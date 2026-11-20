/**
 * Builds the letter file of the ticket e-mail for scripts/render-letter.ts from the TEXT of the real letter.
 *
 *   npx tsx scripts/make-ticket-letter.ts <fields.json> <letter.json>
 *
 * fields.json holds the lines of the letter that the stand sent to the test buyer, copied from its plain-text
 * part, with the test mailbox replaced by carmen@example.com and the organization name by "Aurora Events":
 *   { lang, subject, org, hello, thanks, event: [lines], ticketLabel, ticket, pdf, notes: [paragraphs],
 *     noReply, write, sentTo, seller: [lines], processing, unsubscribe }
 * The layout (colours, blocks, sizes) repeats the HTML of that letter. Links are dead.
 */
import fs from "node:fs";

interface Fields {
  lang: string; subject: string; org: string; hello: string; thanks: string; event: string[]; ticketLabel: string;
  ticket: string; pdf: string; notes: string[]; noReply: string; write: string; sentTo: string; seller: string[];
  processing: string; unsubscribe: string;
}

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

function html(f: Fields): string {
  const p = (t: string) => `<tr><td style="padding:8px 32px;color:#46555f;font-size:14px;line-height:1.6;">${esc(t)}</td></tr>`;
  const [first, ...rest] = f.event;
  const [sellerName, ...sellerRest] = f.seller;
  return `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f2f4f5;padding:24px 0;"><tbody><tr><td align="center">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;width:100%;background:#ffffff;border-radius:8px;overflow:hidden;font-family:Arial,Helvetica,sans-serif;"><tbody>
<tr><td align="center" style="background:#ffffff;padding:20px 32px 10px 32px;font-size:18px;font-weight:bold;color:#22333D;">${esc(f.org)}</td></tr>
<tr><td style="background:#4f46e5;height:3px;font-size:1px;"> </td></tr>
<tr><td style="padding:28px 32px 8px 32px;color:#22333D;font-size:16px;">${esc(f.hello)}</td></tr>
${p(f.thanks)}
<tr><td style="padding:8px 32px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-left:4px solid #4f46e5;background:#f8f9fa;"><tbody><tr><td style="padding:12px 16px;color:#22333D;font-size:14px;line-height:1.6;"><strong>${esc(first)}</strong>${rest.map((l) => `<br/>${esc(l)}`).join("")}</td></tr></tbody></table></td></tr>
<tr><td style="padding:8px 32px;"><div style="font-size:12px;color:#93a0a8;text-transform:uppercase;letter-spacing:0.04em;padding-bottom:4px;">${esc(f.ticketLabel)}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:14px;line-height:1.5;border-collapse:collapse;"><tbody><tr><td style="padding:7px 0;border-bottom:1px solid #eceff1;color:#22333D;">${esc(f.ticket)}<br/><a href="#" style="color:#4f46e5;font-size:13px;">${esc(f.pdf)}</a></td></tr></tbody></table></td></tr>
${f.notes.map(p).join("\n")}
<tr><td style="padding:8px 32px 16px 32px;color:#46555f;font-size:14px;line-height:1.6;border-top:1px solid #eceff1;"><p style="margin:0 0 12px 0;">${esc(f.noReply)}</p><p style="margin:0;"><a href="#" style="display:inline-block;background:#4f46e5;color:#ffffff;text-decoration:none;font-weight:bold;padding:10px 20px;border-radius:6px;">${esc(f.write)}</a></p></td></tr>
<tr><td style="padding:16px 32px 28px 32px;color:#93a0a8;font-size:12px;line-height:1.6;border-top:1px solid #eceff1;">${esc(f.sentTo)}<br/><br/>
<p style="font-size:11px;color:#666;line-height:1.5"><strong>${esc(sellerName)}</strong>${sellerRest.map((l) => `<br/>${esc(l)}`).join("")}</p>
<p style="font-size:11px;color:#999"><a href="#" style="color:inherit;text-decoration:underline">${esc(f.processing)}</a></p>
<p style="font-size:11px;color:#999;line-height:1.5">${esc(f.unsubscribe)}</p></td></tr>
</tbody></table></td></tr></tbody></table>`;
}

const [src, out] = process.argv.slice(2);
if (!src || !out) throw new Error("usage: tsx scripts/make-ticket-letter.ts <fields.json> <letter.json>");
const f = JSON.parse(fs.readFileSync(src, "utf8")) as Fields;
fs.writeFileSync(out, JSON.stringify({ lang: f.lang, subject: f.subject, from: f.org, html: `<body>${html(f)}</body>` }));
console.log(`letter file ${out}`);
