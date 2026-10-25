# Custom Fields & JSONB — Claude rules

_Loaded automatically when Claude reads files under `workflow/`. Moved verbatim from the root CLAUDE.md._

## Custom Fields & JSONB (Data Integrity)
1. **custom_fields JSONB must validate against `workflow_field_definitions` before save** (type, required, enum, regex).
2. **Max 15 custom fields per workflow** — enforce in validator before INSERT/UPDATE.
3. **Never manually craft JSONB.** Use helpers to build `map[string]any`, validate, then marshal.
