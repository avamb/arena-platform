package authemail

import (
	"bytes"
	"fmt"
	"strings"
)

// Ukrainian copy of the platform-user account letters: e-mail verification,
// password reset and account setup / organization invitation. The product is
// "івент-центр"; a show date is "сеанс".

const verifyHTMLUk = `<!DOCTYPE html>
<html lang="uk">
<head><meta charset="UTF-8"><title>Підтвердіть електронну пошту</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Підтвердіть адресу електронної пошти</h1>
  <p>Дякуємо за реєстрацію на платформі ArenaSoldOut.com.</p>
  <p>Натисніть кнопку нижче, щоб підтвердити адресу електронної пошти.
     Посилання дійсне до <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Підтвердити пошту
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Якщо кнопка не працює, скопіюйте це посилання та вставте його в браузер:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Якщо ви не реєструвалися на платформі ArenaSoldOut.com, просто проігноруйте цей лист.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">ArenaSoldOut.com &mdash; автоматичне повідомлення</p>
</body>
</html>`

const resetHTMLUk = `<!DOCTYPE html>
<html lang="uk">
<head><meta charset="UTF-8"><title>Скидання пароля</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Скидання пароля</h1>
  <p>Ми отримали запит на скидання пароля до вашого облікового запису ArenaSoldOut.com.</p>
  <p>Натисніть кнопку нижче, щоб обрати новий пароль.
     Посилання дійсне до <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Скинути пароль
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Якщо кнопка не працює, скопіюйте це посилання та вставте його в браузер:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Якщо ви не запитували скидання пароля, просто проігноруйте цей лист.
    Ваш пароль не зміниться.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">ArenaSoldOut.com &mdash; автоматичне повідомлення</p>
</body>
</html>`

// setupHTMLUk positional arguments: [1]=escaped intro sentence, [2]=expiry,
// [3]=safeURL (href), [4]=safeURL (display).
const setupHTMLUk = `<!DOCTYPE html>
<html lang="uk">
<head><meta charset="UTF-8"><title>Налаштуйте обліковий запис</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Налаштуйте обліковий запис</h1>
  <p>%s</p>
  <p>Натисніть кнопку нижче, щоб обрати пароль і активувати обліковий запис.
     Посилання дійсне до <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Обрати пароль
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Якщо кнопка не працює, скопіюйте це посилання та вставте його в браузер:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Якщо термін дії посилання минув, скористайтеся пунктом «Забули пароль?» на сторінці входу, щоб отримати нове.
    Якщо ви не очікували цього листа, просто проігноруйте його.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">ArenaSoldOut.com &mdash; автоматичне повідомлення</p>
</body>
</html>`

const footerTextUk = "\n--\nArena Platform -- автоматичне повідомлення\n"

func renderVerifyTextUk(rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString("Підтвердіть адресу електронної пошти ArenaSoldOut.com\n\n")
	b.WriteString("Дякуємо за реєстрацію на платформі ArenaSoldOut.com.\n\n")
	fmt.Fprintf(&b, "Перейдіть за посиланням нижче, щоб підтвердити адресу електронної пошти.\nПосилання дійсне до %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("Якщо ви не реєструвалися на платформі ArenaSoldOut.com, просто проігноруйте цей лист.\n")
	b.WriteString(footerTextUk)
	return b.String()
}

func renderResetTextUk(rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString("Скидання пароля ArenaSoldOut.com\n\n")
	b.WriteString("Ми отримали запит на скидання пароля до вашого облікового запису ArenaSoldOut.com.\n\n")
	fmt.Fprintf(&b, "Перейдіть за посиланням нижче, щоб обрати новий пароль.\nПосилання дійсне до %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("Якщо ви не запитували скидання пароля, просто проігноруйте цей лист.\n")
	b.WriteString("Ваш пароль не зміниться.\n")
	b.WriteString(footerTextUk)
	return b.String()
}

func renderSetupTextUk(subject, intro, rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString(subject + "\n\n")
	b.WriteString(intro + "\n\n")
	fmt.Fprintf(&b, "Перейдіть за посиланням нижче, щоб обрати пароль і активувати обліковий запис.\nПосилання дійсне до %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("Якщо термін дії посилання минув, скористайтеся пунктом «Забули пароль?» на сторінці входу, щоб отримати нове.\n")
	b.WriteString("Якщо ви не очікували цього листа, просто проігноруйте його.\n")
	b.WriteString(footerTextUk)
	return b.String()
}

// setupCopyUk returns the subject and intro sentence of a setup letter.
func setupCopyUk(purpose, orgName string) (subject, intro string) {
	orgName = strings.Join(strings.Fields(orgName), " ")
	switch {
	case purpose == PurposeOrgInvitation && orgName != "":
		return "Вас запрошено до " + orgName + " на ArenaSoldOut.com",
			"Вас запрошено приєднатися до " + orgName + " на ArenaSoldOut.com."
	case purpose == PurposeOrgInvitation:
		return "Вас запрошено на ArenaSoldOut.com",
			"Вас запрошено приєднатися до організації на ArenaSoldOut.com."
	default:
		return "Налаштуйте обліковий запис ArenaSoldOut.com",
			"Адміністратор створив для вас обліковий запис ArenaSoldOut.com."
	}
}
