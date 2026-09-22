package mm

import "testing"

func TestMatchAdmissionReplyParsing(t *testing.T) {
	t.Run("exact begin acknowledgement", func(t *testing.T) {
		if !hasExactReply("console noise\nTFMM_MATCH_BEGIN_OK c86aa62fd05a3521\n", "TFMM_MATCH_BEGIN_OK c86aa62fd05a3521") {
			t.Fatal("expected exact acknowledgement")
		}
		if hasExactReply("TFMM_MATCH_BEGIN_OK wrong", "TFMM_MATCH_BEGIN_OK c86aa62fd05a3521") {
			t.Fatal("accepted acknowledgement for another match")
		}
	})

	t.Run("readiness status", func(t *testing.T) {
		if !hasExactReply("TFMM_MATCH_READY_OK c86aa62fd05a3521\n", "TFMM_MATCH_READY_OK c86aa62fd05a3521") {
			t.Fatal("expected readiness acknowledgement")
		}
		if !hasReplyPrefix("TFMM_MATCH_READY_FAILED c86aa62fd05a3521 wrong_lobby\n", "TFMM_MATCH_READY_FAILED") {
			t.Fatal("expected permanent failure")
		}
		if hasReplyPrefix("TFMM_MATCH_READY_PENDING c86aa62fd05a3521 loading_map\n", "TFMM_MATCH_READY_FAILED") {
			t.Fatal("treated pending readiness as permanent failure")
		}
	})
}
