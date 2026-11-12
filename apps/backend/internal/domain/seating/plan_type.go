package seating

// Plan types of seating_plans.plan_type that a geometry can be stored as.
const (
	PlanTypeAssignedSeats    = "assigned_seats"
	PlanTypeGeneralAdmission = "general_admission"
	PlanTypeMixed            = "mixed"
)

// PlanTypeOf picks the plan type the geometry fits: seats only is
// assigned_seats, general-admission areas only is general_admission, both is
// mixed. A geometry with neither is assigned_seats (ValidateForPlanType then
// refuses it for having no seats). The API refuses a plan whose type disagrees
// with its places, so every path that makes a plan from an imported SVG (the
// Telegram bot, the hall plan intake) picks the type here.
func PlanTypeOf(g Geometry) string {
	ga := 0
	for _, c := range g.Categories {
		if c.IsGA() {
			ga++
		}
	}
	switch {
	case ga > 0 && g.SeatCount() > 0:
		return PlanTypeMixed
	case ga > 0:
		return PlanTypeGeneralAdmission
	}
	return PlanTypeAssignedSeats
}
