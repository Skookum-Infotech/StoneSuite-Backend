package controllers

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
)

// orderStockShortages is what the sales order holds that stock cannot cover, for
// the "items short — delivery may be delayed" warning a response carries next to
// the order. An order is never refused for want of stock, so this only informs:
// if it cannot be worked out, the save or read still succeeds and the warning is
// left off (logged, not failed).
func orderStockShortages(r *http.Request, pool *pgxpool.Pool, uuid string) []inventory.Shortage {
	short, err := salesorder.StockShortages(r.Context(), pool, uuid)
	if err != nil {
		slog.Warn("sales order stock shortages unavailable",
			"request_id", middleware.RequestIDFromContext(r.Context()), "sales_order", uuid, "err", err)
		return nil
	}
	return short
}
