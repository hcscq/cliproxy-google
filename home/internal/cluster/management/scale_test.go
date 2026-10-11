package management

import (
	"strings"
	"testing"
)

func TestParseGroupCSV(t *testing.T) {
	doc, err := ParseGroupCSV(strings.NewReader("type,group,member,label,credential_groups\ncredential,g1,a@x.com,,\ncredential,g1,id-2,,\nmodel,m1,gemini-3-flash,,g1;g2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.CredentialGroups) != 1 || len(doc.CredentialGroups[0].Members) != 2 || len(doc.ModelGroups[0].Models[0].CredentialGroups) != 2 {
		t.Fatalf("%+v", doc)
	}
	if _, err := ParseGroupCSV(strings.NewReader("bogus,g,m\n")); err == nil {
		t.Fatal("unknown type must fail")
	}
}
