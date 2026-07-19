package okx

import "testing"

// The code=1 partial-success trap: doRequest returns the data array without error even when
// a per-order sCode signals failure. These guards must catch that so a failed cancel never
// logs a fake success (the bug that left structural backup algos resting on the exchange).
func TestOKXAlgoCancelResultErr(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"success sCode0", `[{"algoId":"123","sCode":"0","sMsg":""}]`, false},
		{"already canceled 51400", `[{"algoId":"123","sCode":"51400","sMsg":"already canceled"}]`, false},
		{"not found 51401", `[{"algoId":"123","sCode":"51401","sMsg":"not exist"}]`, false},
		{"real failure 51000", `[{"algoId":"123","sCode":"51000","sMsg":"param error"}]`, true},
		{"wrong-endpoint reject", `[{"algoId":"123","sCode":"51603","sMsg":"order not found"}]`, true},
		{"empty body", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := okxAlgoCancelResultErr([]byte(c.data), "123")
			if (err != nil) != c.wantErr {
				t.Fatalf("okxAlgoCancelResultErr(%s): err=%v want wantErr=%v", c.data, err, c.wantErr)
			}
		})
	}
}

func TestOKXOrderCancelResultErr(t *testing.T) {
	if err := okxOrderCancelResultErr([]byte(`[{"ordId":"9","sCode":"0"}]`), "9"); err != nil {
		t.Fatalf("sCode0 should succeed: %v", err)
	}
	if err := okxOrderCancelResultErr([]byte(`[{"ordId":"9","sCode":"51510","sMsg":"reject"}]`), "9"); err == nil {
		t.Fatalf("real failure sCode must return error")
	}
}
