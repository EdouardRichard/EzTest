package domain

import "testing"

// TestLimits pins every P3 limit to the value in spec §4.1 (and the field
// bounds from §6.2/§7.2) so a later edit cannot silently widen them.
func TestLimits(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"MaxAttachmentBytes", MaxAttachmentBytes, 10485760},
		{"MaxAttachmentsPerRequirement", MaxAttachmentsPerRequirement, 10},
		{"MaxAttachmentsPerVersion", MaxAttachmentsPerVersion, 50},
		{"MaxAgentImageBytes", MaxAgentImageBytes, 2097152},
		{"MaxAgentImagesPerRun", MaxAgentImagesPerRun, 2},
		{"MaxInjectedTextBytesPerFile", MaxInjectedTextBytesPerFile, 65536},
		{"MaxInjectedTextBytesPerRun", MaxInjectedTextBytesPerRun, 204800},
		{"ImagePartCharEstimate", ImagePartCharEstimate, 4096},
		{"MaxQuestionChars", MaxQuestionChars, 2000},
		{"MaxAnswerChars", MaxAnswerChars, 5000},
		{"MaxDecisionChars", MaxDecisionChars, 5000},
		{"MaxDescriptionChars", MaxDescriptionChars, 20000},
		{"MaxTitleChars", MaxTitleChars, 300},
		{"MaxVersionNameChars", MaxVersionNameChars, 200},
		{"MaxVersionLabelChars", MaxVersionLabelChars, 50},
		{"MaxCodeBranchChars", MaxCodeBranchChars, 200},
		{"MaxAcceptanceCriteriaChars", MaxAcceptanceCriteriaChars, 10000},
		{"MaxTopicChars", MaxTopicChars, 300},
		{"MaxAttachmentFilenameChars", MaxAttachmentFilenameChars, 120},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
