package extmgr

import (
	"fmt"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestBundleCommitRechecksMarketTrustForEveryMember(t *testing.T) {
	for _, failure := range []string{"removed", "invalid-signature", "changed-key", "changed-keys", "changed-spec"} {
		t.Run(failure, func(t *testing.T) {
			h, phone, _ := marketHub(t)
			memberMarket := bundleMarketFixture(t, h, phone, map[string]string{"child": bundleGraphPackage(t, "child", "1.0.0")})
			mainMarket := bundleMarketFixture(t, h, phone, map[string]string{"main": bundleGraphPackage(t, "main", "1.0.0", extension.Dependency{ID: "child"})})
			code, out := call(t, h.routes(), "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"main"}`, mainMarket))
			if code != 200 {
				t.Fatalf("preview %d %v", code, out)
			}
			pv := h.extensions.reg.PreviewOf(out["preview_id"].(string))
			if len(pv.Unresolved) > 0 || len(pv.Bundle) != 1 {
				t.Fatal("incomplete fixture:", pv.Unresolved)
			}
			markets := h.marketsState()
			markets.mu.Lock()
			state := markets.byID[memberMarket]
			switch failure {
			case "removed":
				delete(markets.byID, memberMarket)
			case "invalid-signature":
				state.rec.Signature.State = extension.SigInvalid
				state.rec.Error = "synthetic signature failure"
			case "changed-key":
				state.rec.SigningKey = "synthetic changed public key"
			case "changed-keys":
				state.rec.TrustedKeys = []string{"synthetic added public key"}
			case "changed-spec":
				state.rec.Spec = "github:example/changed-market"
			}
			markets.mu.Unlock()
			if _, err := h.installExtensionPlan(t.Context(), "", pv.ID, pv.Digest, []bundleRef{{PreviewID: pv.Bundle[0].ID, Digest: pv.Bundle[0].Digest}}, "test"); err == nil {
				t.Fatal("changed member market trust accepted")
			}
			if h.extensions.reg.Get("main") != nil || h.extensions.reg.Get("child") != nil {
				t.Fatal("source validation failure wrote state")
			}
			if h.extensions.reg.PreviewOf(pv.ID) == nil || h.extensions.reg.PreviewOf(pv.Bundle[0].ID) == nil {
				t.Fatal("source validation consumed previews")
			}
		})
	}
}

func TestBundleUpgradePhoneCannotIncludeLocalPackage(t *testing.T) {
	h, phone, _ := marketHub(t)
	reg := h.extensions.reg
	first := bundleMarketFixture(t, h, phone, map[string]string{"main": bundleGraphPackage(t, "main", "1.0.0")})
	code, out := call(t, h.routes(), "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"main"}`, first))
	if code != 200 {
		t.Fatalf("preview %d %v", code, out)
	}
	pv := reg.PreviewOf(out["preview_id"].(string))
	if _, err := h.installExtensionPlan(t.Context(), "", pv.ID, pv.Digest, nil, "test"); err != nil {
		t.Fatal(err)
	}
	second := bundleMarketFixture(t, h, phone, map[string]string{"main": bundleGraphPackage(t, "main", "2.0.0", extension.Dependency{ID: "local-child"}), "local-child": bundleGraphPackage(t, "local-child", "1.0.0")})
	code, out = call(t, h.routes(), "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"main","upgrade":"main"}`, second))
	if code != 200 {
		t.Fatalf("preview %d %v", code, out)
	}
	pv = reg.PreviewOf(out["preview_id"].(string))
	local, err := reg.Preview(t.Context(), bundleGraphPackage(t, "local-child", "1.0.0"), "")
	if err != nil {
		t.Fatal(err)
	}
	pv.Bundle = []*extension.Preview{local}
	request := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q,"bundle":[{"preview_id":%q,"permissions_digest":%q}]}`, pv.ID, pv.Digest, local.ID, local.Digest)
	code, out = callFrom(t, h.routes(), "POST", "/api/v1/extensions/main/-/upgrade", phone, request, "203.0.113.1:1234")
	if code != 403 {
		t.Fatalf("phone upgraded with local member: %d %v", code, out)
	}
	if reg.Snapshot("main").Record.Version != "1.0.0" || reg.Get("local-child") != nil {
		t.Fatal("forbidden plan changed state")
	}
}
