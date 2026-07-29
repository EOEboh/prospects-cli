package dedup

import "testing"

func TestDomain(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// The everyday cases: one business, many spellings of its URL.
		{"bare hostname", "acmerecruiting.com", "acmerecruiting.com"},
		{"with scheme", "https://acmerecruiting.com", "acmerecruiting.com"},
		{"with www", "https://www.acmerecruiting.com", "acmerecruiting.com"},
		{"with path", "https://acmerecruiting.com/contact", "acmerecruiting.com"},
		{"with query and fragment", "https://acmerecruiting.com/c?utm=x#top", "acmerecruiting.com"},
		{"uppercase", "HTTPS://WWW.ACMERECRUITING.COM", "acmerecruiting.com"},
		{"trailing whitespace", "  acmerecruiting.com  ", "acmerecruiting.com"},
		{"with port", "https://acmerecruiting.com:8443/", "acmerecruiting.com"},
		{"fully qualified trailing dot", "https://acmerecruiting.com./", "acmerecruiting.com"},
		{"http downgrade is still the same business", "http://acmerecruiting.com", "acmerecruiting.com"},

		// Subdomains of a business's own domain are that business.
		{"blog subdomain folds in", "https://blog.acmerecruiting.com", "acmerecruiting.com"},
		{"careers subdomain folds in", "https://careers.acmerecruiting.com/jobs", "acmerecruiting.com"},

		// Multi-part public suffixes.
		{"co.uk", "https://www.acme.co.uk", "acme.co.uk"},
		{"subdomain on co.uk", "https://shop.acme.co.uk", "acme.co.uk"},
		{"com.au", "https://acme.com.au", "acme.com.au"},

		// Shared platforms: the subdomain IS the business. Folding these to
		// the registrable domain would merge every Wix site into one row.
		{"wix", "https://acmerecruiting.wixsite.com/mysite", "acmerecruiting.wixsite.com"},
		{"weebly", "https://acme.weebly.com", "acme.weebly.com"},
		{"godaddy", "https://acme.godaddysites.com", "acme.godaddysites.com"},
		{"google business site", "https://acme.business.site", "acme.business.site"},
		{"square", "https://acme.square.site", "acme.square.site"},

		// Platforms already on the Public Suffix List resolve the same way
		// without needing an entry in our table.
		{"github pages", "https://acme.github.io", "acme.github.io"},
		{"shopify", "https://acme.myshopify.com", "acme.myshopify.com"},

		// Path-identity hosts: the host identifies the platform, not the
		// business, so the path has to be part of the key.
		{"facebook page", "https://www.facebook.com/AcmeRecruiting", "facebook.com/acmerecruiting"},
		{"facebook page with trailing path", "https://facebook.com/AcmeRecruiting/about", "facebook.com/acmerecruiting"},
		{"linkedin company", "https://www.linkedin.com/company/acme-recruiting", "linkedin.com/company/acme-recruiting"},
		{"google sites", "https://sites.google.com/view/acme", "sites.google.com/view/acme"},
		{"yelp", "https://www.yelp.com/biz/acme-austin", "yelp.com/biz/acme-austin"},
		{"instagram", "https://instagram.com/acmerecruiting", "instagram.com/acmerecruiting"},

		// A bare platform host identifies nothing; better no key than a key
		// that merges every such row together.
		{"bare facebook", "https://facebook.com", ""},
		{"bare facebook with slash", "https://facebook.com/", ""},

		// Absent input is normal, not an error: NameKey takes over.
		{"empty", "", ""},
		{"whitespace only", "   ", ""},

		// Hosts with no registrable domain still produce a stable key.
		{"single label", "http://intranet", "intranet"},
		{"ip address", "http://192.168.1.1/site", "192.168.1.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Domain(tc.input)
			if err != nil {
				t.Fatalf("Domain(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Domain(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Two spellings of the same internationalized domain must produce one key.
func TestDomainNormalizesUnicodeHosts(t *testing.T) {
	unicodeForm, err := Domain("https://bücher.example")
	if err != nil {
		t.Fatalf("Domain(unicode): %v", err)
	}
	punycodeForm, err := Domain("https://xn--bcher-kva.example")
	if err != nil {
		t.Fatalf("Domain(punycode): %v", err)
	}
	if unicodeForm != punycodeForm {
		t.Errorf("unicode %q and punycode %q produced different keys", unicodeForm, punycodeForm)
	}
}

// The point of the shared-host table: these must NOT collapse together.
func TestSharedHostsDoNotCollapse(t *testing.T) {
	groups := [][]string{
		{"https://acme.wixsite.com/site", "https://brightpath.wixsite.com/site"},
		{"https://acme.weebly.com", "https://brightpath.weebly.com"},
		{"https://facebook.com/acme", "https://facebook.com/brightpath"},
		{"https://sites.google.com/view/acme", "https://sites.google.com/view/brightpath"},
	}
	for _, group := range groups {
		a, err := Domain(group[0])
		if err != nil {
			t.Fatalf("Domain(%q): %v", group[0], err)
		}
		b, err := Domain(group[1])
		if err != nil {
			t.Fatalf("Domain(%q): %v", group[1], err)
		}
		if a == b {
			t.Errorf("%q and %q both keyed to %q — two businesses merged into one", group[0], group[1], a)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "Acme Recruiting", "acme recruiting"},
		{"extra whitespace", "  Acme   Recruiting  ", "acme recruiting"},
		{"punctuation", "Acme, Recruiting!", "acme recruiting"},
		{"ampersand", "Smith & Jones", "smith jones"},
		{"hyphen", "Bright-Path Talent", "bright path talent"},
		{"accents", "Café Ampère", "cafe ampere"},

		// Legal suffixes are noise for identity.
		{"inc", "Acme Recruiting Inc", "acme recruiting"},
		{"inc with period", "Acme Recruiting, Inc.", "acme recruiting"},
		{"llc", "Acme Recruiting LLC", "acme recruiting"},
		{"ltd", "Acme Recruiting Ltd.", "acme recruiting"},
		{"stacked suffixes", "Acme Holdings Co Ltd", "acme holdings"},
		{"gmbh", "Acme GmbH", "acme"},

		{"leading the", "The Acme Agency", "acme agency"},
		{"the is kept when it is the whole name", "The", "the"},

		// A name that is only a legal suffix keeps it rather than vanishing.
		{"suffix only", "LLC", "llc"},

		{"digits preserved", "24/7 Staffing", "24 7 staffing"},
		{"empty", "", ""},
		{"punctuation only", "!!!", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeName(tc.input); got != tc.want {
				t.Errorf("NormalizeName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNameKey(t *testing.T) {
	tests := []struct {
		name string
		bn   string
		city string
		want string
	}{
		{"name and city", "Acme Recruiting", "Austin", "acme recruiting|austin"},
		{"city case and spacing", "Acme", "  SAN Antonio ", "acme|san antonio"},
		{"no city", "Acme", "", "acme|"},
		{"no name means no key", "", "Austin", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NameKey(tc.bn, tc.city); got != tc.want {
				t.Errorf("NameKey(%q, %q) = %q, want %q", tc.bn, tc.city, got, tc.want)
			}
		})
	}
}

// Names are only unique locally, so the same name in two cities is two
// prospects, not one.
func TestNameKeyIsScopedByCity(t *testing.T) {
	austin := NameKey("Bright Path Talent", "Austin")
	dallas := NameKey("Bright Path Talent", "Dallas")
	if austin == dallas {
		t.Error("the same business name in two cities produced one key")
	}
}

// Variants that a hand-built CSV realistically contains must converge.
func TestNameKeyMergesVariants(t *testing.T) {
	want := NameKey("Acme Recruiting", "Austin")
	variants := []struct{ name, city string }{
		{"Acme Recruiting, Inc.", "Austin"},
		{"ACME RECRUITING LLC", "austin"},
		{"  Acme   Recruiting  ", " Austin "},
		{"The Acme Recruiting Co.", "Austin"},
	}
	for _, v := range variants {
		if got := NameKey(v.name, v.city); got != want {
			t.Errorf("NameKey(%q, %q) = %q, want %q", v.name, v.city, got, want)
		}
	}
}

func TestKeys(t *testing.T) {
	domain, nameKey, err := Keys("https://www.acme.com", "Acme Recruiting Inc", "Austin")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if domain != "acme.com" {
		t.Errorf("domain = %q, want acme.com", domain)
	}
	if nameKey != "acme recruiting|austin" {
		t.Errorf("nameKey = %q, want %q", nameKey, "acme recruiting|austin")
	}
}

// A broken website must not discard the row: the name key still identifies it.
func TestKeysStillReturnsNameKeyOnBadWebsite(t *testing.T) {
	domain, nameKey, err := Keys("https://exa mple.com/\x7f", "Acme", "Austin")
	if err == nil {
		t.Fatal("a malformed website should be reported")
	}
	if domain != "" {
		t.Errorf("domain = %q, want empty", domain)
	}
	if nameKey != "acme|austin" {
		t.Errorf("nameKey = %q, want acme|austin — the row must survive", nameKey)
	}
}
