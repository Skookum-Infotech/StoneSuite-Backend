package fabrication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"strings"
)

// ErrPurchasePermission rejects procurement without purchase-order create permission.
var ErrPurchasePermission = errors.New("purchase order create permission required")

// ShortageRequirement binds an ordered line to approved measured work.
type ShortageRequirement struct {
	SourceLineID  string  `json:"sourceLineId"`
	Quantity      float64 `json:"quantity"`
	UnitPrice     float64 `json:"unitPrice"`
	ExpectedSlabs *int    `json:"expectedSlabs,omitempty"`
}

// ShortagePurchaseInput describes an operator-confirmed shortage, never inferred from area.
type ShortagePurchaseInput struct {
	CommandMeta
	TemplateID   string                `json:"templateId"`
	VendorID     string                `json:"vendorId"`
	Reason       string                `json:"reason"`
	Requirements []ShortageRequirement `json:"requirements"`
}

// CreateShortagePurchase creates a linked draft using the normal PO approval lifecycle.
func CreateShortagePurchase(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in ShortagePurchaseInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return createShortagePurchase(ctx, pool, jobID, actor, in)
}

func createShortagePurchase(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in ShortagePurchaseInput) (ActionResult, error) {
	var vendor pgtype.UUID
	if err := vendor.Scan(in.VendorID); err != nil || !vendor.Valid {
		return ActionResult{}, ClientError{Msg: "Choose a valid vendor."}
	}
	if actor.EmployeeID <= 0 {
		return ActionResult{}, ErrActionPermission
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Requirements) == 0 {
		return ActionResult{}, ClientError{Msg: "Record the material shortage and requested quantities."}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, ClientError{Msg: "Invalid purchase quantities."}
	}
	var related string
	var created purchaseorder.DraftRef
	result, actionErr := executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "create_shortage_po", SubjectID: in.TemplateID, Payload: payload, PurchasePermission: actor.permission != "", RelatedID: &related}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		job, err := lockJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if job.statusCode == StatusOnHold || IsTerminal(job.statusCode) {
			return ClientError{Msg: "Resume an active job before purchasing material."}
		}
		var templateID int64
		var revision int
		var approved, current int64
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT t.template_id,t.revision,t.sales_order_version,so.sales_order_record_version,t.measured_lines FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id WHERE t.fabrication_job_id=$1 AND t.template_uuid=$2 AND t.state='approved' AND so.sales_order_deleted_at IS NULL FOR SHARE OF t,so`, state.JobID, in.TemplateID).Scan(&templateID, &revision, &approved, &current, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalRequired
		}
		if err != nil {
			return fmt.Errorf("load purchase template: %w", err)
		}
		if approved != current {
			return ErrActionConflict
		}
		var lines []TemplateLine
		if err = json.Unmarshal(raw, &lines); err != nil {
			return fmt.Errorf("decode purchase template: %w", err)
		}
		order := purchaseorder.CreatePurchaseOrderInput{VendorUUID: in.VendorID}
		order.ReferenceNumber = jobID
		order.Notes = in.Reason
		seen := map[string]bool{}
		for index, requirement := range in.Requirements {
			if !positiveFinite(requirement.Quantity) || !nonnegativeFinite(requirement.UnitPrice) || seen[requirement.SourceLineID] {
				return ClientError{Msg: "Use distinct measured lines with positive quantities and valid prices."}
			}
			seen[requirement.SourceLineID] = true
			var material, description string
			for _, line := range lines {
				if line.SourceLineID == requirement.SourceLineID {
					material = line.MaterialID
					description = purchaseRequirementDescription(line, revision)
					break
				}
			}
			if material == "" {
				return ClientError{Msg: "Each purchase line must reference material on the approved template."}
			}
			order.Items = append(order.Items, purchaseorder.LineInput{LineNumber: index + 1, InventoryItemUUID: material, Description: description, Quantity: requirement.Quantity, UnitPrice: requirement.UnitPrice, ExpectedSlabs: requirement.ExpectedSlabs})
		}
		draft, err := purchaseorder.CreateDraftTx(ctx, tx, order, actor.EmployeeID)
		if err != nil {
			var client purchaseorder.ClientError
			if errors.As(err, &client) {
				return ClientError{Msg: client.Error()}
			}
			return fmt.Errorf("create shortage purchase order: %w", err)
		}
		requirements, err := json.Marshal(in.Requirements)
		if err != nil {
			return fmt.Errorf("encode purchase requirements: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO fabrication_shortage_purchase(purchase_order_id,fabrication_job_id,template_id,requirements,reason) VALUES($1,$2,$3,$4,$5)`, draft.InternalID, state.JobID, templateID, requirements, in.Reason); err != nil {
			return fmt.Errorf("link shortage purchase order: %w", err)
		}
		related = draft.ID
		created = draft
		return nil
	})
	if actionErr == nil && created.ID != "" {
		purchaseorder.NotifyDraftCreated(ctx, pool, created, actor.EmployeeID)
	}
	return result, actionErr
}
