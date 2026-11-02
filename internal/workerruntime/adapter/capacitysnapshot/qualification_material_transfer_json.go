package capacitysnapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func qualificationDecodeTransfer(raw []byte, v *qualificationTransfer) error {
	if len(raw) == 0 || len(raw) > 2*MaxBytes {
		return errQualification
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return errQualification
	}
	// A canonical wrapper avoids duplicate/case-folded-key ambiguity without
	// extending the strict production JSON reader's original aggregate limit.
	canonical, e := json.Marshal(v)
	if e != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return fmt.Errorf("noncanonical transfer")
	}
	return nil
}
func qualificationWriteTransfer(w io.Writer, v qualificationTransfer) error {
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > 2*MaxBytes {
		return errQualification
	}
	n, e := w.Write(raw)
	if e != nil {
		return e
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}
