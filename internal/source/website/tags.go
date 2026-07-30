package website

import "github.com/EOEboh/prospects-cli/internal/model"

// tagSignature matches a third-party tool by the fingerprints it leaves in page
// source: a script host, a widget class, an inline global.
//
// Needles are matched case-insensitively against the raw HTML. They are chosen
// to be specific enough that a passing mention in prose cannot trigger them —
// "we use Mailchimp" in a blog post is not a Mailchimp integration.
type tagSignature struct {
	Value   string
	Kind    model.SignalType
	Needles []string
	// Why explains the match in the score breakdown, so an explanation reads
	// as an observation rather than an assertion.
	Why string
}

// automationSignatures identify marketing, CRM and scheduling tools. Their
// presence is what makes a prospect *less* interesting: a business already
// running Calendly has solved part of the problem being sold.
var automationSignatures = []tagSignature{
	{
		Value: "hubspot", Kind: model.TypeAutomationTag,
		Needles: []string{"js.hs-scripts.com", "js.hsforms.net", "hs-analytics.net",
			"hsforms.com", "_hsq.push", "js.hubspot.com"},
		Why: "HubSpot tracking or forms",
	},
	{
		Value: "calendly", Kind: model.TypeAutomationTag,
		Needles: []string{"assets.calendly.com", "calendly.com/", "calendly-badge", "Calendly.init"},
		Why:     "Calendly scheduling",
	},
	{
		Value: "intercom", Kind: model.TypeAutomationTag,
		Needles: []string{"widget.intercom.io", "intercomcdn.com", "intercomSettings", "js.intercomcdn.com"},
		Why:     "Intercom messenger",
	},
	{
		Value: "mailchimp", Kind: model.TypeAutomationTag,
		Needles: []string{"list-manage.com", "chimpstatic.com", "mc.us", "mailchimp.com/subscribe"},
		Why:     "Mailchimp signup",
	},
	{
		Value: "typeform", Kind: model.TypeAutomationTag,
		Needles: []string{"embed.typeform.com", "typeform.com/to/", "data-tf-widget"},
		Why:     "Typeform embed",
	},
	{
		Value: "activecampaign", Kind: model.TypeAutomationTag,
		Needles: []string{"activehosted.com", "prism.app-us1.com"},
		Why:     "ActiveCampaign automation",
	},
	{
		Value: "klaviyo", Kind: model.TypeAutomationTag,
		Needles: []string{"static.klaviyo.com", "klaviyo.js", "_learnq"},
		Why:     "Klaviyo email marketing",
	},
	{
		Value: "convertkit", Kind: model.TypeAutomationTag,
		Needles: []string{"convertkit.com/", "ck.page", "f.convertkit.com"},
		Why:     "ConvertKit email marketing",
	},
	{
		Value: "jotform", Kind: model.TypeAutomationTag,
		Needles: []string{"form.jotform.com", "jotfor.ms", "js.jotform.com"},
		Why:     "Jotform form",
	},
	{
		Value: "gravityforms", Kind: model.TypeAutomationTag,
		Needles: []string{"gravityforms/js", "gform_wrapper", "gform_submit"},
		Why:     "Gravity Forms",
	},
	{
		Value: "zapier", Kind: model.TypeAutomationTag,
		Needles: []string{"zapier.com/embed", "zapier-interfaces"},
		Why:     "Zapier embed",
	},
	{
		Value: "acuity", Kind: model.TypeAutomationTag,
		Needles: []string{"acuityscheduling.com", "squarespacescheduling.com"},
		Why:     "Acuity scheduling",
	},
	{
		Value: "pipedrive", Kind: model.TypeAutomationTag,
		Needles: []string{"pipedriveassets.com", "webforms.pipedrive.com"},
		Why:     "Pipedrive CRM",
	},
	{
		Value: "zoho", Kind: model.TypeAutomationTag,
		Needles: []string{"zohopublic.com", "crm.zoho.com", "salesiq.zoho"},
		Why:     "Zoho CRM",
	},
}

// chatSignatures identify live chat widgets. A staffed chat widget means
// inbound messages are already being handled by a person, which changes the
// pitch.
var chatSignatures = []tagSignature{
	{Value: "intercom", Kind: model.TypeChatWidget, Needles: []string{"widget.intercom.io", "intercomSettings"}, Why: "Intercom chat"},
	{Value: "drift", Kind: model.TypeChatWidget, Needles: []string{"js.driftt.com", "drift.load", "driftt.com"}, Why: "Drift chat"},
	{Value: "tawk", Kind: model.TypeChatWidget, Needles: []string{"embed.tawk.to", "tawk.to/"}, Why: "Tawk.to chat"},
	{Value: "crisp", Kind: model.TypeChatWidget, Needles: []string{"client.crisp.chat", "$crisp"}, Why: "Crisp chat"},
	{Value: "livechat", Kind: model.TypeChatWidget, Needles: []string{"cdn.livechatinc.com", "__lc.license"}, Why: "LiveChat"},
	{Value: "zendesk", Kind: model.TypeChatWidget, Needles: []string{"static.zdassets.com", "zopim.com", "zEmbed"}, Why: "Zendesk chat"},
	{Value: "tidio", Kind: model.TypeChatWidget, Needles: []string{"code.tidio.co", "tidiochat"}, Why: "Tidio chat"},
	{Value: "olark", Kind: model.TypeChatWidget, Needles: []string{"static.olark.com", "olark.identify"}, Why: "Olark chat"},
	{Value: "freshchat", Kind: model.TypeChatWidget, Needles: []string{"wchat.freshchat.com", "fcWidget"}, Why: "Freshchat"},
	{Value: "hubspot", Kind: model.TypeChatWidget, Needles: []string{"js.usemessages.com", "hubspot-messages-iframe"}, Why: "HubSpot chat"},
	{Value: "facebook", Kind: model.TypeChatWidget, Needles: []string{"fb-customerchat", "connect.facebook.net/en_US/sdk/xfbml.customerchat"}, Why: "Facebook Messenger chat"},
}

// enterpriseSignatures indicate a business large enough to build this in-house,
// which is why they score negatively. An applicant tracking system or a
// marketing automation suite implies a dedicated team and a procurement
// process — the opposite of the 2-to-50-person target.
var enterpriseSignatures = []tagSignature{
	{
		Value: "salesforce", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"salesforce.com/", "force.com", "pardot.com", "d.la1-c1cs", "web-to-lead"},
		Why:     "Salesforce or Pardot",
	},
	{
		Value: "marketo", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"marketo.net", "munchkin.js", "mktoForms2"},
		Why:     "Marketo marketing automation",
	},
	{
		Value: "workday", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"myworkdayjobs.com", "workday.com/"},
		Why:     "Workday HR platform",
	},
	{
		Value: "greenhouse", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"boards.greenhouse.io", "greenhouse.io/embed"},
		Why:     "Greenhouse applicant tracking",
	},
	{
		Value: "lever", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"jobs.lever.co", "lever.co/postings"},
		Why:     "Lever applicant tracking",
	},
	{
		Value: "eloqua", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"eloqua.com", "elqCfg"},
		Why:     "Oracle Eloqua",
	},
	{
		Value: "adobe-experience", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"adobedtm.com", "demdex.net", "omtrdc.net"},
		Why:     "Adobe Experience Cloud",
	},
	{
		Value: "sap", Kind: model.TypeEnterpriseMarker,
		Needles: []string{"successfactors.com", "sapsf.com"},
		Why:     "SAP SuccessFactors",
	},
}
