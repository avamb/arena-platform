package authemail

import (
	"strings"
	"testing"
)

func TestRenderAdminNewApplication(t *testing.T) {
	p := OnboardingEmailPayload{
		Kind: OnboardingKindAdminNewApplication, OrgName: "  Vino <&> Co ",
		Title:   "Новая заявка на онбординг",
		Details: []string{"Организация: Vino <&> Co", "Страна: ES", "Система одобрила бы: да"},
		Link:    "https://admin.example.test/onboarding/abc",
	}
	subject, htmlBody, textBody, ok := renderOnboardingEmail(p, p.Link, false)
	if !ok {
		t.Fatal("admin_new_application is not rendered")
	}
	if subject != "Новая заявка на онбординг: Vino <&> Co" {
		t.Fatalf("subject = %q", subject)
	}
	for _, want := range []string{"Страна: ES", "Система одобрила бы: да", "Что делать", "https://admin.example.test/onboarding/abc"} {
		if !strings.Contains(textBody, want) {
			t.Errorf("text lacks %q: %s", want, textBody)
		}
	}
	if strings.Contains(htmlBody, "Vino <&>") || !strings.Contains(htmlBody, "Vino &lt;&amp;&gt; Co") {
		t.Errorf("html does not escape the organization: %s", htmlBody)
	}
	if !strings.Contains(htmlBody, `href="https://admin.example.test/onboarding/abc"`) {
		t.Errorf("html lacks the link: %s", htmlBody)
	}
}
