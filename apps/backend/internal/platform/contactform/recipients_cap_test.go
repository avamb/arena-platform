package contactform

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func recipientsJSON(t *testing.T, topic string, n int) []byte {
	t.Helper()
	list := make([]string, n)
	for i := range list {
		list[i] = fmt.Sprintf("owner%d@example.com", i)
	}
	b, err := json.Marshal(map[string][]string{topic: list})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// One topic is mailed to at most MaxRecipientsPerTopic addresses: a write
// above it is refused, and a stored list above it is cut on read.
func TestRecipients_CappedPerTopic(t *testing.T) {
	if msg := ValidateRecipients(recipientsJSON(t, TopicContact, MaxRecipientsPerTopic)); msg != "" {
		t.Fatalf("ten addresses refused: %s", msg)
	}
	if msg := ValidateRecipients(recipientsJSON(t, TopicContact, MaxRecipientsPerTopic+1)); !strings.Contains(msg, "at most 10") {
		t.Fatalf("eleven addresses: %q, want a refusal", msg)
	}
	if msg := ValidateRecipients(nil); msg != "" {
		t.Fatalf("absent recipients refused: %s", msg)
	}
	if msg := ValidateRecipients([]byte(`["a@example.com"]`)); msg == "" {
		t.Fatal("a list instead of a topic map was accepted")
	}
	got := RecipientsFor(recipientsJSON(t, TopicPrivateEvent, 25), TopicPrivateEvent)
	if len(got) != MaxRecipientsPerTopic || got[0] != "owner0@example.com" {
		t.Fatalf("read of 25 stored addresses = %v, want the first %d", got, MaxRecipientsPerTopic)
	}
}
