package capacitysnapshot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func knownAdmissionPolicy() *workerapp.CapacityGuaranteeRef {
	return &workerapp.CapacityGuaranteeRef{ID: "m1_accounted_capacity_v1", Revision: 1, Digest: "sha256:badb64addbd9fbeff2f74978887f4d3c592d587f0cd47d72efeb40dd48b5f5cd"}
}

func TestTrustAdmissionPolicyKnown(t *testing.T) {
	tr, _, _ := fixture(t)
	tr.AdmissionPolicy = knownAdmissionPolicy()
	if err := ValidateTrust(tr); err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Trust
	if err := strictJSON(raw, &decoded); err != nil {
		t.Fatalf("valid strict Trust rejected: %v", err)
	}
	if decoded.AdmissionPolicy == nil || *decoded.AdmissionPolicy != *knownAdmissionPolicy() {
		t.Fatalf("reference not retained: %+v", decoded.AdmissionPolicy)
	}
}

func admissionPolicyJSON(t *testing.T) string {
	t.Helper()
	return string(admissionMarshal(t, knownAdmissionPolicy()))
}

func admissionMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func admissionTrustPath(t *testing.T, raw []byte) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrustAdmissionPolicyLegacyCompatibility(t *testing.T) {
	const golden = `{"key_id":"","public_key":null,"manifest_digest":"","identity":{"engine_id":"","docker_root":"","fs_uuid":"","guest_boot_id":"","mount_namespace":"","mount_id":0,"device":""},"anchor_path":"","core":null,"storage":null,"coverage":null}`
	if got := string(admissionMarshal(t, Trust{})); got != golden {
		t.Fatalf("legacy field order/omission changed: %s", got)
	}
	tr, _, _ := fixture(t)
	raw := admissionMarshal(t, tr)
	if bytes.Contains(raw, []byte("admission_policy")) {
		t.Fatal("legacy marshal supplied default policy")
	}
	var decoded Trust
	if err := strictJSON(raw, &decoded); err != nil {
		t.Fatal("legacy strict decode", err)
	}
	if decoded.AdmissionPolicy != nil {
		t.Fatal("legacy strict decode defaulted policy")
	}
	read, err := ReadTrust(admissionTrustPath(t, raw))
	if err != nil {
		t.Fatal("legacy reader", err)
	}
	if read.AdmissionPolicy != nil {
		t.Fatal("legacy reader defaulted policy")
	}
	if !bytes.Equal(admissionMarshal(t, read), raw) || !bytes.Equal(admissionMarshal(t, decoded), raw) {
		t.Fatal("legacy reader roundtrip changed bytes")
	}
	if err := ValidateTrust(tr); err != nil || tr.AdmissionPolicy != nil {
		t.Fatal("legacy validation changed policy", err)
	}
}

func TestTrustAdmissionPolicyReaderRetainsKnownReference(t *testing.T) {
	tr, _, _ := fixture(t)
	tr.AdmissionPolicy = knownAdmissionPolicy()
	raw := admissionMarshal(t, tr)
	suffix := `,"admission_policy":` + admissionPolicyJSON(t) + `}`
	if !strings.HasSuffix(string(raw), suffix) {
		t.Fatal("reference is not the final Trust field")
	}
	read, err := ReadTrust(admissionTrustPath(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if read.AdmissionPolicy == nil || *read.AdmissionPolicy != *knownAdmissionPolicy() {
		t.Fatal("reader lost reference")
	}
	if !bytes.Equal(raw, admissionMarshal(t, read)) {
		t.Fatal("reader rewrote known reference")
	}
	if err := ValidateTrust(read); err != nil {
		t.Fatal(err)
	}
}

func TestTrustAdmissionPolicyValidateIndependently(t *testing.T) {
	for name, change := range map[string]func(*workerapp.CapacityGuaranteeRef){
		"zero_struct":      func(r *workerapp.CapacityGuaranteeRef) { *r = workerapp.CapacityGuaranteeRef{} },
		"empty_id":         func(r *workerapp.CapacityGuaranteeRef) { r.ID = "" },
		"unknown_id":       func(r *workerapp.CapacityGuaranteeRef) { r.ID = "other" },
		"zero_revision":    func(r *workerapp.CapacityGuaranteeRef) { r.Revision = 0 },
		"wrong_revision":   func(r *workerapp.CapacityGuaranteeRef) { r.Revision = 2 },
		"max_revision":     func(r *workerapp.CapacityGuaranteeRef) { r.Revision = ^uint64(0) },
		"empty_digest":     func(r *workerapp.CapacityGuaranteeRef) { r.Digest = "" },
		"zero_digest":      func(r *workerapp.CapacityGuaranteeRef) { r.Digest = "sha256:" + strings.Repeat("0", 64) },
		"tampered_digest":  func(r *workerapp.CapacityGuaranteeRef) { r.Digest = r.Digest[:len(r.Digest)-1] + "e" },
		"wrongcase_digest": func(r *workerapp.CapacityGuaranteeRef) { r.Digest = strings.ToUpper(r.Digest) },
		"newline_digest":   func(r *workerapp.CapacityGuaranteeRef) { r.Digest += "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			tr, _, _ := fixture(t)
			if err := ValidateTrust(tr); err != nil {
				t.Fatal("invalid legacy positive control", err)
			}
			tr.AdmissionPolicy = knownAdmissionPolicy()
			change(tr.AdmissionPolicy)
			before := *tr.AdmissionPolicy
			if err := ValidateTrust(tr); err == nil {
				t.Fatal("invalid reference accepted against otherwise-valid Trust")
			}
			if *tr.AdmissionPolicy != before {
				t.Fatal("validation changed invalid reference")
			}
		})
	}
}

func TestTrustAdmissionPolicyStrictMutations(t *testing.T) {
	tr, _, _ := fixture(t)
	tr.AdmissionPolicy = knownAdmissionPolicy()
	raw := string(admissionMarshal(t, tr))
	ref := admissionPolicyJSON(t)
	var control Trust
	if err := strictJSON([]byte(raw), &control); err != nil {
		t.Fatal("strict positive control", err)
	}
	if _, err := ReadTrust(admissionTrustPath(t, []byte(raw))); err != nil {
		t.Fatal("reader positive control", err)
	}
	// Semantic reference errors still use valid JSON/schema; their rejection is
	// owned by ValidateTrust and the reader, not the schema-only strict decoder.
	type mutation struct {
		raw         string
		schemaValid bool
	}
	cases := map[string]mutation{}
	nested := func(name, value string, schemaValid bool) {
		cases[name] = mutation{strings.Replace(raw, `"admission_policy":`+ref, `"admission_policy":`+value, 1), schemaValid}
	}
	for name, value := range map[string]string{"null": "null", "empty_object": "{}", "array": "[]", "object_array": "[" + ref + "]", "string": `"policy"`, "number": "1", "true": "true", "false": "false"} {
		nested("nested_"+name, value, false)
	}
	for _, field := range []string{"id", "revision", "digest"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ref), &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, field)
		nested("missing_"+field, string(admissionMarshal(t, fields)), false)
	}
	for name, replace := range map[string][2]string{
		"empty_id":         {`"id":"m1_accounted_capacity_v1"`, `"id":""`},
		"unknown_id":       {`"id":"m1_accounted_capacity_v1"`, `"id":"other"`},
		"zero_revision":    {`"revision":1`, `"revision":0`},
		"wrong_revision":   {`"revision":1`, `"revision":2`},
		"max_revision":     {`"revision":1`, `"revision":18446744073709551615`},
		"empty_digest":     {`"digest":"` + knownAdmissionPolicy().Digest + `"`, `"digest":""`},
		"zero_digest":      {`"digest":"` + knownAdmissionPolicy().Digest + `"`, `"digest":"sha256:` + strings.Repeat("0", 64) + `"`},
		"tampered_digest":  {`"digest":"` + knownAdmissionPolicy().Digest + `"`, `"digest":"` + knownAdmissionPolicy().Digest[:70] + `e"`},
		"wrongcase_digest": {`"digest":"` + knownAdmissionPolicy().Digest + `"`, `"digest":"` + strings.ToUpper(knownAdmissionPolicy().Digest) + `"`},
		"newline_digest":   {`"digest":"` + knownAdmissionPolicy().Digest + `"`, `"digest":"` + knownAdmissionPolicy().Digest + `\n"`},
	} {
		nested(name, strings.Replace(ref, replace[0], replace[1], 1), true)
	}
	for _, field := range []string{"id", "revision", "digest"} {
		key := `"` + field + `":`
		escaped := `"\u` + map[string]string{"id": "0069d", "revision": "0072evision", "digest": "0064igest"}[field] + `":`
		value := map[string]string{"id": `"m1_accounted_capacity_v1"`, "revision": "1", "digest": `"` + knownAdmissionPolicy().Digest + `"`}[field]
		nested("null_"+field, strings.Replace(ref, key+value, key+"null", 1), false)
		nested("alias_"+field, strings.Replace(ref, key, `"`+strings.ToUpper(field)+`":`, 1), false)
		nested("alias_extra_"+field, strings.TrimSuffix(ref, "}")+`,"`+strings.ToUpper(field)+`":`+value+`}`, false)
		nested("duplicate_"+field, strings.Replace(ref, key+value, key+value+","+key+value, 1), false)
		nested("escaped_duplicate_"+field, strings.Replace(ref, key+value, key+value+","+escaped+value, 1), false)
	}
	nested("inner_unknown", strings.TrimSuffix(ref, "}")+`,"allow":true}`, false)
	for name, value := range map[string]string{"negative": "-1", "fraction": "1.5", "exponent": "1e0", "string_number": `"1"`, "overflow": "18446744073709551616", "bool_number": "true", "array_number": "[]", "object_number": "{}"} {
		nested(name, strings.Replace(ref, `"revision":1`, `"revision":`+value, 1), false)
	}
	nested("id_number", strings.Replace(ref, `"id":"m1_accounted_capacity_v1"`, `"id":1`, 1), false)
	nested("digest_number", strings.Replace(ref, `"digest":"`+knownAdmissionPolicy().Digest+`"`, `"digest":1`, 1), false)
	cases["outer_unknown"] = mutation{strings.TrimSuffix(raw, "}") + `,"allow":true}`, false}
	cases["outer_policy_alias"] = mutation{strings.Replace(raw, `"admission_policy":`, `"ADMISSION_POLICY":`, 1), false}
	cases["outer_policy_alias_extra"] = mutation{strings.TrimSuffix(raw, "}") + `,"ADMISSION_POLICY":` + ref + `}`, false}
	cases["outer_policy_duplicate"] = mutation{strings.TrimSuffix(raw, "}") + `,"admission_policy":` + ref + `}`, false}
	cases["outer_policy_escaped_duplicate"] = mutation{strings.TrimSuffix(raw, "}") + `,"\u0061dmission_policy":` + ref + `}`, false}
	cases["outer_legacy_duplicate"] = mutation{strings.Replace(raw, `"key_id":"key"`, `"key_id":"key","key_id":"key"`, 1), false}
	cases["outer_legacy_escaped_duplicate"] = mutation{strings.Replace(raw, `"key_id":"key"`, `"key_id":"key","\u006bey_id":"key"`, 1), false}
	for _, field := range []string{"key_id", "public_key", "manifest_digest", "identity", "anchor_path", "core", "storage", "coverage"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, field)
		cases["outer_missing_"+field] = mutation{string(admissionMarshal(t, fields)), false}
		cases["outer_alias_"+field] = mutation{strings.Replace(raw, `"`+field+`":`, `"`+strings.ToUpper(field)+`":`, 1), false}
	}
	for name, value := range map[string]string{"null": "null", "empty": "{}", "array": "[]", "object_array": "[" + raw + "]", "string": `"trust"`, "number": "1", "true": "true"} {
		cases["outer_"+name] = mutation{value, false}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.raw == raw {
				t.Fatal("mutation ineffective")
			}
			var decoded Trust
			err := strictJSON([]byte(tc.raw), &decoded)
			if tc.schemaValid {
				if err != nil {
					t.Fatal("semantic mutation failed before validation", err)
				}
				if err := ValidateTrust(decoded); err == nil {
					t.Fatal("semantic mutation accepted")
				}
			} else if err == nil {
				t.Fatal("malformed strict schema accepted")
			}
			if _, err := ReadTrust(admissionTrustPath(t, []byte(tc.raw))); err == nil {
				t.Fatal("reader accepted mutation")
			}
		})
	}
}

func admissionSigned(t *testing.T, key ed25519.PrivateKey, payload []byte, version, domain string) []byte {
	t.Helper()
	// Literal version/domain arguments are deliberately independent of the
	// production constants. Every mutation is signed anew to reach schema checks.
	signature := ed25519.Sign(key, append([]byte(domain), payload...))
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), append([]byte(domain), payload...), signature) {
		t.Fatal("synthetic signature control failed")
	}
	return admissionMarshal(t, Envelope{Version: version, KeyID: "key", Payload: payload, Signature: signature})
}

func TestTrustAdmissionPolicySignedSchemasUnchanged(t *testing.T) {
	if Version != "switchback.capacity.raw.v1" || SignatureDomain != "switchback/capacity/raw/v1\x00" || VersionV2 != "switchback.capacity.raw.v2" || SignatureDomainV2 != "switchback/capacity/raw/v2\x00" {
		t.Fatal("raw protocol identity changed")
	}
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			var tr Trust
			var key ed25519.PrivateKey
			var payload []byte
			protocol, domain := "switchback.capacity.raw.v1", "switchback/capacity/raw/v1\x00"
			if version == "v1" {
				var f Facts
				tr, key, f = fixture(t)
				payload = admissionMarshal(t, f)
			} else {
				tr, key, payload = inodeV2Fixture(t)
				protocol, domain = "switchback.capacity.raw.v2", "switchback/capacity/raw/v2\x00"
			}
			verify := func(raw []byte, trust Trust) ([]byte, []byte, []byte, error) {
				if version == "v1" {
					v, err := Verify(raw, trust)
					return v.Raw(), v.Payload(), v.Signature(), err
				}
				v, err := VerifyEvidenceV2(raw, trust)
				return v.Raw(), v.Payload(), v.Signature(), err
			}
			original := admissionSigned(t, key, payload, protocol, domain)
			beforeRaw, beforePayload, beforeSignature, err := verify(original, tr)
			if err != nil {
				t.Fatal("legacy signed positive control", err)
			}
			tr.AdmissionPolicy = knownAdmissionPolicy()
			afterRaw, afterPayload, afterSignature, err := verify(original, tr)
			if err != nil {
				t.Fatal("metadata rejected unchanged signed payload", err)
			}
			if !bytes.Equal(beforeRaw, original) || !bytes.Equal(afterRaw, original) || !bytes.Equal(beforePayload, payload) || !bytes.Equal(afterPayload, payload) || !bytes.Equal(beforeSignature, afterSignature) {
				t.Fatal("metadata altered original signed bytes")
			}
			mutations := map[string]string{"injected_admission_policy": strings.TrimSuffix(string(payload), "}") + `,"admission_policy":` + admissionPolicyJSON(t) + `}`}
			expected := map[string]string{"injected_admission_policy": "unknown field admission_policy"}
			for _, field := range []string{"sequence", "observed_at", "identity", "manifest_digest", "engine_cpu_milli", "engine_memory_bytes", "anchor", "core", "storage", "containers", "removals"} {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				if _, ok := fields[field]; !ok {
					t.Fatalf("missing positive control field %s", field)
				}
				delete(fields, field)
				mutations["missing_"+field] = string(admissionMarshal(t, fields))
				expected["missing_"+field] = "missing required field " + field
			}
			for name, mutated := range mutations {
				t.Run(name, func(t *testing.T) {
					if bytes.Equal(payload, []byte(mutated)) {
						t.Fatal("mutation ineffective")
					}
					raw := admissionSigned(t, key, []byte(mutated), protocol, domain)
					_, _, _, err := verify(raw, tr)
					if err == nil || !strings.Contains(err.Error(), expected[name]) {
						t.Fatalf("did not reach intended schema rejection: %v", err)
					}
				})
			}
		})
	}
}

func TestTrustAdmissionPolicyPreservesCreatedLayerInodeGuard(t *testing.T) {
	for _, defaultHard := range []uint64{0, 10000} {
		name := "zero_default"
		if defaultHard > 0 {
			name = "positive_default"
		}
		t.Run(name, func(t *testing.T) {
			for _, accounted := range []bool{false, true} {
				tr, _, facts, request, identity := createdFixture(t)
				if accounted {
					tr.AdmissionPolicy = knownAdmissionPolicy()
					tr.ManifestDigest = Digest([]byte("distinct accounted created-layer manifest"))
					facts.ManifestDigest = tr.ManifestDigest
				}
				verifier, err := NewCreatedLayerVerifier(tr)
				if err != nil {
					t.Fatal(err)
				}
				if err := verifier.VerifyCreatedLayerFacts(context.Background(), facts, request, identity); err != nil {
					t.Fatal("positive target-inode control", err)
				}
				binary.LittleEndian.PutUint64(facts.Quota.Target[24:32], 0)
				binary.LittleEndian.PutUint64(facts.Quota.Default0[24:32], defaultHard)
				err = verifier.VerifyCreatedLayerFacts(context.Background(), facts, request, identity)
				if accounted {
					if err != nil {
						t.Fatalf("exact accounted policy rejected finite zero target: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "target inode hard limit unproven") {
					t.Fatalf("legacy nil reference bypassed target-inode guard: %v", err)
				}
			}
		})
	}
}
