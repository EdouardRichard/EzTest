package quality

// Operational metric names of the quality overview (spec §5.2, architecture
// §16). They are the only names the read model exposes.
const (
	OperationalRequirementClosureRate   = "requirement_closure_rate"
	OperationalCaseConfirmationRate     = "case_confirmation_rate"
	OperationalExecutionPassRate        = "execution_pass_rate"
	OperationalIssueValidityRate        = "issue_validity_rate"
	OperationalEvidenceCompletenessRate = "evidence_completeness_rate"
	OperationalContextCompactionCount   = "context_compaction_count"
	OperationalTaskFailureRetryRate     = "task_failure_retry_rate"
)

// OperationalMetricOrder is the stable display order of the seven metrics.
var OperationalMetricOrder = []string{
	OperationalRequirementClosureRate,
	OperationalCaseConfirmationRate,
	OperationalExecutionPassRate,
	OperationalIssueValidityRate,
	OperationalEvidenceCompletenessRate,
	OperationalContextCompactionCount,
	OperationalTaskFailureRetryRate,
}

var operationalDefinitions = map[string]string{
	OperationalRequirementClosureRate:   "已闭环需求 / 可闭环需求（草稿+已澄清+已闭环，排除已废弃）",
	OperationalCaseConfirmationRate:     "当前已确认用例版本 / 当前用例版本（草稿+已确认+过期）",
	OperationalExecutionPassRate:        "通过用例运行 / 已结束用例运行（通过+失败+阻塞+未定）",
	OperationalIssueValidityRate:        "已确认问题 / 已确认+已驳回问题（排除未处理/已合并）",
	OperationalEvidenceCompletenessRate: "含至少一条证据的已结束用例运行 / 已结束用例运行",
	OperationalContextCompactionCount:   "context.compacted 事件计数（版本频道或全局）",
	OperationalTaskFailureRetryRate:     "重试次数大于 1 的工作项 / 已尝试工作项",
}

// OperationalMetric is the wire-independent read model of one operational KPI.
// Value is nil when the denominator is zero (available=false); the UI must
// render "unavailable" instead of a fabricated zero.
type OperationalMetric struct {
	Name        string
	Value       *float64
	Numerator   int
	Denominator int
	Available   bool
	Definition  string
}

// OperationalSnapshot is the seven-metric read model returned by the metrics
// aggregator (Task 6). The seven metrics are always present and ordered.
type OperationalSnapshot struct {
	Metrics []OperationalMetric
}

// OperationalDefinition returns the documented definition of a metric, used as
// the UI tooltip and contract description.
func OperationalDefinition(name string) string {
	return operationalDefinitions[name]
}

// NewOperationalMetric builds a ratio metric. A zero denominator yields
// available=false with value nil, while numerator/denominator stay visible.
func NewOperationalMetric(name string, numerator, denominator int) OperationalMetric {
	metric := OperationalMetric{
		Name:        name,
		Numerator:   numerator,
		Denominator: denominator,
		Definition:  OperationalDefinition(name),
	}
	if denominator > 0 {
		value := float64(numerator) / float64(denominator)
		metric.Value = &value
		metric.Available = true
	}
	return metric
}

// NewOperationalCount builds a count metric. Counts are always available (a
// real zero is data, unlike a missing denominator); the denominator is 1 so the
// contract's ratio shape still holds.
func NewOperationalCount(name string, count int) OperationalMetric {
	return NewOperationalMetric(name, count, 1)
}

// UnavailableOperationalMetrics is the seven-metric placeholder used when no
// aggregator is wired (Task 5 before Task 22 wiring). Every metric is
// unavailable, so the page shows no fabricated zeros.
func UnavailableOperationalMetrics() []OperationalMetric {
	metrics := make([]OperationalMetric, 0, len(OperationalMetricOrder))
	for _, name := range OperationalMetricOrder {
		metrics = append(metrics, NewOperationalMetric(name, 0, 0))
	}
	return metrics
}
