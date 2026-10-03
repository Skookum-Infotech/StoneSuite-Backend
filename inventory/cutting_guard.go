package inventory

import (
	"context"
	"fmt"
)

// Caller holds the slab lock (or its bundle lock for bundle-only changes).
// Cutting start takes those same locks before persisting its input records.
func requireOutsideCutting(ctx context.Context, q pgxQuerier, slabID int) error {
	var active bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_cutting_input i
 JOIN fabrication_cutting_run r USING(cutting_run_id)
 JOIN fabrication_job_slab a USING(fabrication_job_slab_id)
 WHERE a.inventory_slab_id=$1 AND r.state='started')`, slabID).Scan(&active)
	if err != nil {
		return fmt.Errorf("check active cutting input: %w", err)
	}
	if active {
		return ClientError{Msg: "This unit is in an active cutting run. Complete cutting or record its measured disposition before changing inventory."}
	}
	return nil
}

func requireBundleOutsideCutting(ctx context.Context, q pgxQuerier, bundleID int) error {
	var active bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_cutting_input i
 JOIN fabrication_cutting_run r USING(cutting_run_id)
 JOIN fabrication_job_slab a USING(fabrication_job_slab_id)
 JOIN inventory_slab s USING(inventory_slab_id)
 WHERE s.inventory_bundle_id=$1 AND r.state='started')`, bundleID).Scan(&active)
	if err != nil {
		return fmt.Errorf("check bundle cutting inputs: %w", err)
	}
	if active {
		return ClientError{Msg: "This bundle contains material in an active cutting run. Complete cutting or record its measured disposition first."}
	}
	return nil
}
