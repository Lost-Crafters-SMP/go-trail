package render

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
)

// Fingerprints lock the bytes emitted by toon-go, not handwritten TOON syntax.
func TestTOONMachineCanonical(t *testing.T) {
	for _, tc := range []struct {
		name   string
		doc    Object
		golden string
	}{
		{"inspect", serializationSample("inspect", 0, 0, 0), "d4d07a0164e280a62cb79a53377de38ea8f35a71ac727f1f0a30f982c377bd66"},
		{"trace", benchmarkDocument(3, 2, 1), "d0606d6a3c08741d4d9544f32ff3bf2feb9db15f2ec2b981be263b689df28703"},
		{"span", Object{"span": benchmarkDocument(1, 2, 1)["trace"].(Object)["spans"].([]Object)[0]}, "437d1e7a4302169dea23059f708d90b95644db68559049ae10ce26a3ede0d952"},
		{"query", serializationSample("query100", 3, 0, 0), "9e7eff6aa5bc00e08f428c9e1542decd65200c6d0f51c6033f2b406dfa375936"},
		{"export", Object{"capture": Object{"traces": []Object{benchmarkDocument(3, 2, 1)["trace"].(Object)}}}, "af1021a890a7fb80d5bda8c567c7edd56cd46569768d4bcb93866a4129fb5739"},
		{"loss", serializationSample("diagnosticsLossHeavy", 0, 0, 0), "b7a0018f86f032d35f060d8016162bba0a73e53449fdbe97237c6c1271dfcd6c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Write(&out, "toon", tc.doc); err != nil {
				t.Fatal(err)
			}
			got := fmt.Sprintf("%x", sha256.Sum256(out.Bytes()))
			if got != tc.golden {
				t.Fatalf("canonical output changed: sha256=%s\n%s", got, out.String())
			}
		})
	}
}
