package eventnote

// JobTypeTestEmail is the worker job that mails the organizer a sample ticket
// letter carrying the event's note, so they can see how buyers will read it
// (POST /v1/organizations/{org_id}/events/{event_id}/email-note/test).
//
// The letter is NOT a buyer letter: it is sent straight through the e-mail
// sender and is never written to email_letters, which holds the evidence of
// what buyers received (plan 3a, rule 3).
const JobTypeTestEmail = "event.email_note_test"

// TestPayload is the job payload. Note is already filtered by Normalize and
// may be the text of an unsaved draft; To has been checked by the API to be
// the caller's own address or that of an active member of the organization.
type TestPayload struct {
	OrgID   string `json:"org_id"`
	EventID string `json:"event_id"`
	To      string `json:"to"`
	Locale  string `json:"locale,omitempty"`
	Note    string `json:"note"`
}

// TestEmailsPerHour is the cap of test letters per organization per hour.
const TestEmailsPerHour = 5
