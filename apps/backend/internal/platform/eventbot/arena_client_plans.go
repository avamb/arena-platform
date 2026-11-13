package eventbot

// arena_client_plans.go — what the wizard asks of the client about seating
// plans. A plan is made by an approved intake (arena_client_plan_intake.go);
// the bot never creates one directly.

// errNoPlacesStored: a plan has no current version with a category that has
// places (the plan question could not use it).
const errNoPlacesStored = planUploadError("seating plan has no category with places")

type planUploadError string

func (e planUploadError) Error() string { return string(e) }

// CanUploadPlans reports that this client takes the files of a plan: the wizard
// offers the upload only to a reference source that can do it.
func (c *ArenaClient) CanUploadPlans() bool { return true }
