package assistant

// The periodic resource advice is an AI task like any other (a schedule, a question, a report that is kept), made for one purpose: every day the model reads the analysis of
// the resource dashboard of the modules that most need a look and writes a short report, which the dashboard shows. The task is read-only: the model has the read-only tools of the
// assistant and changes nothing; the figures are the tool's, so the report explains them and does not work them out.

// AdviceTaskName is the name of the task.
const AdviceTaskName = "Resource advice"

// DefaultAdviceSchedule is when the report is made: every day at 8 o'clock of the platform's zone.
const DefaultAdviceSchedule = "0 8 * * *"

// AdvicePrompt is the question the task puts to the model each day.
func AdvicePrompt(lang string) string {
	if lang == "en" {
		return `Write the daily advice on the Requests, Limits and replicas of the modules.
1. Call analyze_resources once with limit 20. Use nothing else for the figures.
2. First line: how many modules were read (read), and how many are over-provisioned, under-provisioned, limited or unknown: quote the counts of the tool (counts), do not count them yourself. Name any module that could not be read (skipped) and why.
3. Then, for each module that has something to change (a verdict over, under or limited), a short block: the module and its namespace, CPU and memory with the Requests and Limits of one Pod now, the P95 use, the recommended Requests and Limits, the replicas now against the replicas the use asks for, and the reasons in a few words. Modules whose verdicts are all ok get one line together.
Rules: every number comes from the tool, never work one out or round it differently; a result whose certain is false is not certain: say so with its caution, and say a Request can be lowered only where can_lower is true; say that Requests and Limits from the Pod template of Kubernetes are the real ones and "estimated" ones are not; this is a report only: do not say that anything was changed and do not offer to change anything. Plain Markdown, no more than a page.`
	}
	return `请写每日的资源建议：各模块的 Requests、Limits 和副本数。
1. 只调用一次 analyze_resources，limit 取 20；数字全部来自它，不用别的来源。
2. 第一行：读了多少个模块（read），其中超配、欠配、受限、无法判断的各有多少：直接引用工具给的 counts，不要自己数；没读到的模块（skipped）写明是哪个、为什么。
3. 然后对每个有要调整的模块（判定为 over、under 或 limited）写一小段：模块和命名空间；CPU 与内存现在单 Pod 的 Requests、Limits、P95 使用量、推荐的 Requests 和 Limits；现在的副本数和使用量需要的副本数；用几个词写原因。判定都是 ok 的模块合写一行。
规则：每个数字都来自工具，不要自己计算，也不要换一种取整；certain 为 false 的结论要说不确定并写出 caution；只有 can_lower 为 true 才能说可以调低；Pod 配置来自 Kubernetes 的是真实值，“estimated”（推算）的不是，要写明；这只是一份报告：不要说改了什么，也不要提出替你去改。用普通 Markdown，不超过一页。`
}
