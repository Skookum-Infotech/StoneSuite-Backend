package inventory

// bin_history.go — what has happened to a bin: slabs moved in, slabs moved out,
// and the other events that put a unit into or took it out of the bin.
//
// There is no table of its own. inventory_unit_history already records the bin
// on each side of every movement (from_bin_id / to_bin_id), so a bin's history is
// a read over it -- which also means every route into and out of a bin shows up
// here, whether it came from a bin transfer, a fabrication WIP move, a bundle or
// a receipt, not only the moves made through one document.

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Bin history directions.
const (
	BinHistoryIn    = "in"    // the unit arrived in the bin
	BinHistoryOut   = "out"   // the unit left the bin
	BinHistoryOther = "other" // the bin is named on the event but the unit neither arrived nor left
)

const (
	binHistoryDefaultLimit = 50
	binHistoryMaxLimit     = 100
)

// BinHistoryRequest pages a bin's movement history, newest first.
type BinHistoryRequest struct {
	// Cursor is opaque: pass back the previous page's NextCursor, never build one.
	Cursor string
	Limit  int
	// Direction narrows to arrivals ("in") or departures ("out"); empty means all.
	Direction string
}

// BinHistoryEntry is one event on a bin.
type BinHistoryEntry struct {
	// Action is the unit-history action: bin_move, create, cut, scrap,
	// warehouse_move, remnant_created...
	Action    string `json:"action"`
	Direction string `json:"direction"`

	UnitID            string `json:"unitId"`
	UnitSerial        string `json:"unitSerial"`
	InventoryItemName string `json:"inventoryItemName,omitempty"`

	// FromBinPath and ToBinPath are the two ends of the movement; whichever is the
	// bin being read is the bin itself.
	FromBinPath string `json:"fromBinPath,omitempty"`
	ToBinPath   string `json:"toBinPath,omitempty"`

	Note string `json:"note,omitempty"`
	// TransferID and TransferNumber name the bin transfer that made the move, when
	// one did.
	TransferID     string `json:"transferId,omitempty"`
	TransferNumber string `json:"transferNumber,omitempty"`

	At     string `json:"at"`
	ByName string `json:"byName"`
}

// BinHistoryPage is one page of a bin's history.
type BinHistoryPage struct {
	Records    []BinHistoryEntry `json:"records"`
	NextCursor string            `json:"nextCursor"`
	HasMore    bool              `json:"hasMore"`
}

// BinHistory returns the movement history of one bin, newest first.
func BinHistory(ctx context.Context, pool *pgxpool.Pool, binUUID string, req BinHistoryRequest) (BinHistoryPage, error) {
	b, err := binByUUID(ctx, pool, binUUID, false)
	if err != nil {
		return BinHistoryPage{}, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = binHistoryDefaultLimit
	}
	if limit > binHistoryMaxLimit {
		limit = binHistoryMaxLimit
	}

	where := []string{"(h.to_bin_id = $1 OR h.from_bin_id = $1)"}
	args := []any{b.id}
	switch req.Direction {
	case "":
	case BinHistoryIn:
		where = append(where, "h.to_bin_id = $1 AND h.from_bin_id IS DISTINCT FROM h.to_bin_id")
	case BinHistoryOut:
		where = append(where, "h.from_bin_id = $1 AND h.to_bin_id IS DISTINCT FROM h.from_bin_id")
	default:
		return BinHistoryPage{}, ClientError{Msg: "direction must be \"in\" or \"out\"."}
	}
	if req.Cursor != "" {
		id, err := decodeBinHistoryCursor(req.Cursor)
		if err != nil {
			return BinHistoryPage{}, ClientError{Msg: "Invalid cursor."}
		}
		args = append(args, id)
		where = append(where, fmt.Sprintf("h.inventory_unit_history_id < $%d", len(args)))
	}
	args = append(args, limit+1)

	rows, err := pool.Query(ctx, `
		SELECT h.inventory_unit_history_id, h.history_action,
		       CASE WHEN h.to_bin_id = $1 AND h.from_bin_id IS DISTINCT FROM h.to_bin_id THEN 'in'
		            WHEN h.from_bin_id = $1 AND h.to_bin_id IS DISTINCT FROM h.from_bin_id THEN 'out'
		            ELSE 'other' END,
		       s.inventory_slab_uuid, s.slab_serial, COALESCE(ii.inventory_item_name,''),
		       COALESCE(fb.bin_path,''), COALESCE(tb.bin_path,''), h.history_note,
		       COALESCE(bt.bin_transfer_uuid::text,''), COALESCE(bt.bin_transfer_number,''),
		       to_char(h.history_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM inventory_unit_history h
		JOIN inventory_slab s       ON s.inventory_slab_id = h.inventory_slab_id
		JOIN inventory_item ii      ON ii.inventory_item_id = s.inventory_item_id
		LEFT JOIN inventory_bin fb  ON fb.inventory_bin_id = h.from_bin_id
		LEFT JOIN inventory_bin tb  ON tb.inventory_bin_id = h.to_bin_id
		LEFT JOIN bin_transfer bt   ON bt.bin_transfer_id = h.bin_transfer_id
		LEFT JOIN employee e        ON e.employee_id = h.history_by
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY h.inventory_unit_history_id DESC
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return BinHistoryPage{}, fmt.Errorf("load bin history: %w", err)
	}
	defer rows.Close()

	var (
		out    = []BinHistoryEntry{}
		lastID int
		more   bool
	)
	for rows.Next() {
		var (
			id int
			e  BinHistoryEntry
		)
		if err := rows.Scan(&id, &e.Action, &e.Direction, &e.UnitID, &e.UnitSerial, &e.InventoryItemName,
			&e.FromBinPath, &e.ToBinPath, &e.Note, &e.TransferID, &e.TransferNumber, &e.At, &e.ByName); err != nil {
			return BinHistoryPage{}, fmt.Errorf("scan bin history: %w", err)
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, e)
		lastID = id
	}
	if err := rows.Err(); err != nil {
		return BinHistoryPage{}, fmt.Errorf("load bin history: %w", err)
	}

	page := BinHistoryPage{Records: out, HasMore: more}
	if more {
		page.NextCursor = encodeBinHistoryCursor(lastID)
	}
	return page, nil
}

func encodeBinHistoryCursor(id int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(id)))
}

func decodeBinHistoryCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(raw))
}
