package mailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"construct/source/internal/config"
)

const logoURL = "https://lisaos.dev/accounts.png"

type emailRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
}

func send(cfg *config.Config, to, subject, html, text string) error {
	if cfg.DeliveryURL == "" || cfg.DeliveryAPIKey == "" {
		log.Printf("[DEV] Email to %s: %s", to, subject)
		return nil
	}

	payload := emailRequest{
		From:    cfg.EmailFrom,
		To:      to,
		Subject: subject,
		HTML:    html,
		Text:    text,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.DeliveryURL+"/api/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.DeliveryAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("delivery API returned %d", resp.StatusCode)
	}

	log.Printf("[email] Sent '%s' to %s", subject, to)
	return nil
}

func button(url, label string) string {
	return `<table cellpadding="0" cellspacing="0" border="0" style="margin:0 0 28px">
		<tr><td style="border-radius:10px;background:#FF2D55" align="center">
			<a href="` + url + `" target="_blank" style="display:inline-block;padding:14px 40px;color:#ffffff;font-family:Rubik,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;font-size:13px;font-weight:600;text-decoration:none;text-transform:uppercase;letter-spacing:1px">` + label + `</a>
		</td></tr>
	</table>`
}

func renderEmail(title, content string) string {
	return `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
<meta http-equiv="Content-Type" content="text/html; charset=UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1.0"/>
<meta name="color-scheme" content="light"/>
<meta name="supported-color-schemes" content="light"/>
<title>` + title + `</title>
<!--[if !mso]><!-->
<link href="https://fonts.googleapis.com/css2?family=Rubik:wght@400;500;600&display=swap" rel="stylesheet"/>
<!--<![endif]-->
</head>
<body style="margin:0;padding:0;background-color:#f3f4f6;-webkit-font-smoothing:antialiased;font-family:Rubik,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">

<table width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#f3f4f6">
<tr><td align="center" style="padding:48px 16px">

<table width="520" cellpadding="0" cellspacing="0" border="0" style="max-width:520px;width:100%">

<!-- Logo -->
<tr><td style="padding:0 0 24px">
	<img src="` + logoURL + `" alt="Construct" width="180" height="42" style="display:block;border:0;outline:none"/>
</td></tr>

<!-- Card -->
<tr><td style="background-color:#ffffff;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden">
<table width="100%" cellpadding="0" cellspacing="0" border="0">

<tr><td style="padding:40px 40px 0">
	<h1 style="margin:0 0 24px;font-size:24px;font-weight:600;color:#111827;font-family:Rubik,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">` + title + `</h1>
</td></tr>

<tr><td style="padding:0 40px 40px">
	` + content + `
</td></tr>

</table>
</td></tr>

<!-- Footer -->
<tr><td style="padding:28px 0 0" align="center">
	<p style="margin:0 0 8px;font-size:12px;color:#9ca3af;font-family:Rubik,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.5">
		This is an automated message — please do not reply. Need help? Visit <a href="https://lisaos.dev/support" style="color:#9ca3af;text-decoration:underline">Construct Support</a>.
	</p>
	<p style="margin:0;font-size:12px;color:#d1d5db;font-family:Rubik,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">
		<a href="https://lisaos.dev" style="color:#d1d5db;text-decoration:none">lisaos.dev</a>
	</p>
</td></tr>

</table>

</td></tr>
</table>

</body>
</html>`
}

// SendMemberAdded notifies a user they've been added to an organization.
func SendMemberAdded(cfg *config.Config, to, addedByName, orgName, role string) error {
	html := renderEmail(
		"Welcome to "+orgName,
		`<p style="margin:0 0 24px;color:#374151;font-size:15px;line-height:1.6">
			<strong>`+addedByName+`</strong> added you to <strong>`+orgName+`</strong> on Construct as <strong>`+role+`</strong>.
		</p>`+
			button("construct://app", "Open Construct")+
			`<p style="margin:0;color:#9ca3af;font-size:13px;line-height:1.6">
			Sign in with this email address to access your organization's projects, providers, and spaces.
		</p>`,
	)

	text := fmt.Sprintf("%s added you to %s on Construct as %s.\n\nSign in with this email address to get started.", addedByName, orgName, role)

	return send(cfg, to, "You've been added to "+orgName, html, text)
}

// SendOrgInvite sends an organization invitation email with a 6-char code and deep link.
func SendOrgInvite(cfg *config.Config, to, inviterName, orgName, token, code string) error {
	deepLink := "construct://org/invite/" + token

	html := renderEmail(
		"You're invited",
		`<p style="margin:0 0 24px;color:#374151;font-size:15px;line-height:1.6">
			<strong>`+inviterName+`</strong> invited you to join <strong>`+orgName+`</strong> on Construct.
		</p>`+
			button(deepLink, "Open in Construct")+
			`<p style="margin:0 0 8px;color:#9ca3af;font-size:13px">Or enter this code in Construct:</p>
		<p style="margin:0 0 32px;font-size:32px;font-weight:700;letter-spacing:8px;color:#111827;font-family:'Courier New',monospace;text-align:center">`+code+`</p>
		<p style="margin:0;color:#9ca3af;font-size:13px;line-height:1.6">
			This invitation expires in 7 days. If you weren't expecting this, you can safely ignore it.
		</p>`,
	)

	text := fmt.Sprintf("%s invited you to join %s on Construct.\n\nOpen in Construct: %s\n\nOr enter this code: %s\n\nThis invitation expires in 7 days.", inviterName, orgName, deepLink, code)

	return send(cfg, to, inviterName+" invited you to "+orgName, html, text)
}
