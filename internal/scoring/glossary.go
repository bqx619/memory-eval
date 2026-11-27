package scoring

import (
	"slices"
	"strings"
)

// MetricDef is the single source of presentation semantics. Null direction is
// intentional for counts/distributions where greater is not intrinsically better.
type MetricDef struct {
	ID             string   `json:"id"`
	Group          string   `json:"group"`
	NameZh         string   `json:"name_zh"`
	DefinitionZh   string   `json:"definition_zh"`
	Formula        string   `json:"formula"`
	Range          string   `json:"range"`
	HigherIsBetter *bool    `json:"higher_is_better"`
	Aggregation    string   `json:"aggregation"`
	Caveats        []string `json:"caveats"`
	AppliesTo      []string `json:"applies_to"`
}

func Glossary() []MetricDef {
	out := []MetricDef{}
	add := func(id, group, name, definition, formula, span string, direction *bool, aggregation string, caveats, applies []string) {
		if caveats == nil {
			caveats = []string{}
		}
		if applies == nil {
			applies = []string{}
		}
		out = append(out, MetricDef{id, group, name, definition, formula, span, direction, aggregation, caveats, applies})
	}
	up, down := Bool(true), Bool(false)
	retrieval := []struct{ id, name, def, formula string }{
		{"hit", "证据命中率", "top-k 至少命中一条 gold 证据。", "I(|P∩G|>0)"},
		{"recall", "证据召回率", "top-k 内命中的 gold 证据占全部 gold 的比例。", "|P∩G|/|G|"},
		{"mrr", "首条证据倒数排名", "首个 gold 命中的名次倒数；无命中为 0。", "1/first_hit_rank（无命中为 0）"},
		{"ndcg", "归一化折损累计增益", "命中位置的折损增益相对理想排序。", "Σ I(hit_i)/log2(i+1) / Σ(j=1…min(|G|,k)) 1/log2(j+1)"},
		{"all_evidence", "证据完整率", "top-k 是否包含全部 gold 证据。", "I(G⊆P)"},
		{"precision", "证据精确率", "仅对 exhaustive gold 计算命中数占返回条数的比例。", "|P∩G|/|P|；有 gold 而返回空集为 0"},
		{"empty_result_correct", "空结果正确率", "gold 为空且 exhaustive 时，正确返回空集。", "I(|P|=0)"},
	}
	for _, m := range retrieval {
		agg := "scored 且 gold 非空的用例均值；n 为非 null 样本数"
		caveat := []string{"引用按首次出现去重后截断 top-k；证据单位依套件 event/session；缺失值不当作 0。"}
		if m.id == "precision" {
			agg = "仅 exhaustive 且该指标非 null 的用例均值"
			caveat = append(caveat, "源码兼容规则：gold 与返回集均为空时 precision=null；无 gold 而返回非空为 0。不可假定 n 等于 planned。")
		}
		if m.id == "empty_result_correct" {
			agg = "仅 gold 为空且 exhaustive 的用例均值"
		}
		add(m.id, "memory-only 检索", m.name, m.def, m.formula, "[0,1]", up, agg, caveat, []string{"retrieval", "qa", "typed_context"})
		for _, layer := range []string{"pinnedFacts", "relevantBeliefs", "openQuestions", "personaPrelude"} {
			add("typed_context."+layer+"."+m.id, "PMB 类型化", layer+" "+m.name, "按 gold.expect 对该层校验；"+m.def, m.formula, "[0,1]", up, agg, []string{"层级别名只在报告实际提供该值时展示；personaPrelude 的文本断言不能凭空换算成检索分数。"}, []string{"precisionmembench/typed_context"})
		}
	}
	add("pinned_coverage", "PMB 类型化", "置顶覆盖率", "pinnedFacts 中覆盖 mustInclude 的比例。", "|pinned∩mustInclude|/|mustInclude|", "[0,1]", up, "mustInclude 非空的 scored 用例均值", nil, []string{"precisionmembench/typed_context"})
	for _, unit := range []string{"turn", "session"} {
		for _, metric := range []string{"recall_any", "recall_all", "ndcg_any"} {
			formula := map[string]string{"recall_any": "I(至少一个 gold 在检索前缀中)", "recall_all": "I(所有 gold 在检索前缀中)", "ndcg_any": "DCG/IDCG；首位权重 1，其后 1/log2(i+1)，i 从 1 起"}[metric]
			add("longmemeval_official."+unit+"."+metric+"@k", "LongMemEval 官方", map[string]string{"turn": "轮次", "session": "会话"}[unit]+" "+map[string]string{"recall_any": "任一证据命中率", "recall_all": "完整召回率", "ndcg_any": "排序增益"}[metric], "官方 eval_utils.py 的 "+unit+" 级检索指标。", formula, "[0,1]", up, "定义值的用例均值；保留 n", []string{"与通用 memory-only 不同，不能混比。", "空 gold 的 recall_all 按官方空集合规则为 1；session 前缀扩展至 k 个不同会话，排名与理想语料保留重复 session。", "turn 当前输出 @5/@10/@50；session 当前输出 recall_all/ndcg_any 的 @5/@10，session.recall_any 仅预留。"}, []string{"longmemeval"})
		}
	}
	mean := "scored 且该指标非 null 的样本均值；按类别另报，不跨基准混比"
	simple := []struct{ id, name, def, formula string }{
		{"judge", "裁判正确率", "Jev 按基准官方说明作封闭判定；不代表官方原模型。", "Σ correct/n"},
		{"qa_accuracy", "问答正确率", "LongMemEval 裁判正确标签的比例。", "Σ I(label=true)/n"},
		{"token_f1", "词元 F1", "按绑定基准的分词与规范化计算答案词元重合。", "2PR/(P+R)"},
		{"exact_match", "完全匹配率", "答案按该基准规范化后与任一参考完全相同。", "max_gt I(normalize(pred)=normalize(gt))"},
		{"mcq", "选项正确率", "基准解析器提取的选项是否为 gold 选项。", "I(parsed_choice=gold)"},
		{"mcq.correct", "选项正确率", "PersonaMem/PrefEval 官方解析规则后的选项命中。", "I(parsed_choice=gold)"},
		{"choice_accuracy", "选项匹配诊断", "协议 MCQ choice 字段与 gold 直接比较。", "I(choice=gold)"},
		{"exact_match_diagnostic", "完全匹配诊断", "通用答案规范化后与参考的完全匹配，仅为诊断。", "max_gt I(normalize(pred)=normalize(gt))"},
		{"token_f1_diagnostic", "词元 F1 诊断", "通用规范化词袋重合的最大 F1，仅为诊断。", "max_gt 2PR/(P+R)"},
		{"nugget", "要点覆盖", "按协议的参考要点与权重聚合。", "以绑定协议的 nugget 规则为准"},
		{"assertions", "断言通过率", "所有有效源断言均成立的用例比例；无有效断言为 unscored。", "Σ I(assertions_passed=assertions)/n_with_pass_judgment"},
		{"rouge", "ROUGE", "绑定基准指定的 n-gram 或最长公共子序列重合。", "2PR/(P+R)；具体变体见协议"},
		{"locomo_official.f1", "LoCoMo F1", "cat1–4 的官方词干词元 F1；cat3 采用官方首项规则。", "官方 category 分支的 token F1"},
		{"locomo_official.abstain_acc", "LoCoMo 拒答准确率", "cat5 根据官方拒答短语判断。", "I(命中官方拒答短语)"},
		{"locomo_official.overall", "LoCoMo 总体得分", "cat1–4 F1 与 cat5 拒答正确率合并。", "Σ category_score/n"},
		{"beam.llm_judge_score", "BEAM 条目均分", "每题 rubric 条目裁判等级 0/0.5/1 的均值。", "mean(item_score)"},
		{"beam.event_ordering.precision", "事件集合精确率", "经等价对齐后预测事件集合的精确率。", "|P∩G|/|P|"},
		{"beam.event_ordering.recall", "事件集合召回率", "经等价对齐后 gold 事件集合的覆盖率。", "|P∩G|/|G|"},
		{"beam.event_ordering.f1", "事件集合 F1", "事件集合精确率与召回率的调和均值。", "2PR/(P+R)"},
		{"beam.event_ordering.tau_norm", "顺序一致性归一值", "含并列修正的 Kendall τ-b 转换到 0–1。", "(τ_b+1)/2"},
		{"beam.event_ordering.final", "事件顺序最终分", "归一化 Kendall τ-b 乘事件集合 F1。", "(τ_b+1)/2 × F1"},
		{"memora.mpa", "记忆保留准确率", "应保留子问题回答正确的比例；无保留项时 0。", "p/n_p；n_p=0 时 0"},
		{"memora.faa", "遗忘遵从准确率", "应遗忘子问题回答正确的比例；无遗忘项时 1。", "f/n_f；n_f=0 时 1"},
		{"memora.fama", "遗忘调整记忆分", "MPA 减去按遗忘项占比加权的 FAA 错误惩罚。", "max(0,MPA−λ(1−FAA)), λ=n_f/(n_p+n_f)"},
		{"memora.question_correct", "题目完全正确率", "本题全部子问题是否判断正确。", "I(所有子问题正确)"},
		{"convomem.correct", "ConvoMem 正确率", "按 default/rubric/abstention 模板判定 right。", "I(right)"},
		{"convomem.correct.n_evidence.*", "证据规模分组正确率", "按 n_evidence 分组的 ConvoMem 正确率。", "Σ I(right)/n_group"},
	}
	for _, m := range simple {
		add(m.id, "fixed_reader", m.name, m.def, m.formula, "[0,1]", up, mean, []string{"解析/裁判失败不计 0，不进入有效分母；结合覆盖率阅读。"}, []string{"fixed_reader"})
	}
	add("beam.event_ordering.tau_b", "fixed_reader", "Kendall τ-b", "排序相关系数，含并列修正。", "(concordant−discordant)/sqrt((pairs−ties_x)(pairs−ties_y))", "[-1,1]", up, mean, []string{"无可比较顺序时 NaN 输出 null，final 同样无定义。"}, []string{"beam"})
	for i := 1; i <= 5; i++ {
		id := "locomo_official.f1.cat" + string(rune('0'+i))
		add(id, "fixed_reader", "LoCoMo 类别得分", "对应 category 的官方分数；cat5 实际为拒答准确率。", "mean(category_score)", "[0,1]", up, mean, nil, []string{"locomo"})
	}
	for _, id := range []string{"mab_exact_match", "mab_substring_exact_match", "mab_f1", "mab_eventqa_recall", "mab_ruler_recall", "mab_recsys_recall@k", "mab_longmemeval_judge", "mab_infbench_sum.fluency"} {
		defs := map[string]string{"mab_exact_match": "规范化后的完全匹配，取参考答案最大值。", "mab_substring_exact_match": "规范化后的参考是否为回答子串，取参考最大值。", "mab_f1": "规范化词元 F1，取参考最大值；yes/no/noanswer 不匹配时 0。", "mab_eventqa_recall": "所有参考答案都作为子串出现才为 1。", "mab_ruler_recall": "原始回答命中的参考答案子串比例。", "mab_recsys_recall@k": "电影候选匹配后，前 k 项覆盖 gold 的比例。", "mab_longmemeval_judge": "LongMemEval 模板 Jev 二值判定。", "mab_infbench_sum.fluency": "官方 fluency 说明的 Jev 二值判定。"}
		names := map[string]string{"mab_exact_match": "完全匹配率", "mab_substring_exact_match": "子串匹配率", "mab_f1": "词元 F1", "mab_eventqa_recall": "事件问答完整召回率", "mab_ruler_recall": "答案子串召回率", "mab_recsys_recall@k": "推荐候选召回率", "mab_longmemeval_judge": "问答裁判正确率", "mab_infbench_sum.fluency": "摘要流畅度"}
		add(id, "MemoryAgentBench", names[id], defs[id], "绑定 checker 的官方分支，按有效样本求均值", "[0,1]", up, mean, nil, []string{"memoryagentbench"})
	}
	for _, typ := range []string{"rouge1", "rouge2", "rougeL", "rougeLsum"} {
		for _, part := range []string{"f1", "recall"} {
			for _, prefix := range []string{"", "mab_", "mab_infbench_sum."} {
				add(prefix+typ+"_"+part, "fixed_reader", typ+" "+part, "官方 ROUGE "+typ+"；按词元 n-gram 或 LCS 重合。", map[string]string{"f1": "2PR/(P+R)", "recall": "overlap/reference_units"}[part], "[0,1]", up, mean, []string{"不同分词/词干/换行规则的 ROUGE 不可混比；MAB 参考取最大值。"}, []string{"memoryagentbench"})
			}
		}
	}
	for _, m := range []struct{ id, def, formula string }{
		{"preference_following_accuracy", "没有任何一种偏好跟随错误。", "!(U || I || H || A)"},
		{"error.unhelpful", "helpful 探针为 no。", "U=!helpful"},
		{"error.inconsistent", "知道偏好且未编造，但回答违背偏好且并非不帮助。", "I=acknow && !hallucinate && violate && helpful"},
		{"error.hallucination_of_preference_violation", "复述或归因了编造偏好并违背原偏好。", "H=acknow && hallucinate && violate && helpful"},
		{"error.preference_unaware_violation", "未意识到偏好，且回答违背偏好。", "A=!acknow && violate && helpful"},
	} {
		d := down
		if m.id == "preference_following_accuracy" {
			d = up
		}
		names := map[string]string{"preference_following_accuracy": "偏好遵从准确率", "error.unhelpful": "无帮助错误率", "error.inconsistent": "不一致错误率", "error.hallucination_of_preference_violation": "偏好编造且违背错误率", "error.preference_unaware_violation": "未意识偏好而违背错误率"}
		add("prefeval."+m.id, "PrefEval", names[m.id], m.def, m.formula, "[0,1]", d, mean, []string{"acknow=no 时 hallucinate 不计入；四探针任一失败则本题 invalid。"}, []string{"prefeval/generation"})
		if m.id == "preference_following_accuracy" {
			add(m.id, "PrefEval", "偏好遵从准确率", m.def, m.formula, "[0,1]", d, mean, nil, []string{"prefeval/generation"})
		}
	}
	add("decision.*", "评测器决策诊断", "命名决策数值", "decide 命令的命名问题数值：noul 为 P(true)，choice 为所选选项概率，score 为原生等级期望。", "noul=P(true)；choice=P(selected)；score=E[level]", "noul/choice [0,1]；score [0,levels−1]", nil, "按同名问题对有效重复样本求均值", []string{"不是记忆质量分；名称来自输入问题，不代表正确率；不同类型和等级数不能混比。"}, []string{"decision"})
	out = append(out, haluGlossary()...)
	for _, m := range []struct {
		id, name, def, span string
		dir                 *bool
	}{
		{"status_counts", "状态分布", "按 scored/invalid/error/unsupported 等统计的样本数。", "非负整数映射", nil},
		{"scoring_coverage", "评分覆盖率", "scored 样本占 planned 的比例；有错误时必须和分数一起看。", "[0,1]", up},
		{"execution_errors", "执行错误数", "error + contract_error + ingest_failed 的样本数。", "非负整数", down},
		{"invalid", "无效结果数", "无效引用（不存在、cutoff 后、重复等）、空阅读器答案或基准解析失败；不能视作 0 分。", "非负整数", down},
		{"unenforced_isolation", "隔离未生效", "报告未通过要求的隔离核验，不可作为受隔离的评分结果。", "布尔值", down},
		{"unsupported", "不支持数", "缺少所需能力或未实现的用例数量。", "非负整数", down},
		{"reader_empty_answers", "空答案数", "阅读器无内容而标 invalid 的样本数。", "非负整数", down},
		{"untraceable_hits", "不可追溯命中数", "检索命中缺少可核验来源的累计数量。", "非负整数", down},
		{"planned", "计划样本数", "该报告 summary 的样本总数，含 repeats；中断时查看 interruption。", "非负整数", nil},
		{"n_with_pass_judgment", "有效通过判断数", "scored 且 passed 非 null 的样本数。", "非负整数", nil},
		{"passed", "通过数", "scored 且 passed=true 的样本数。", "非负整数", nil},
		{"pass_rate_on_scored", "断言通过率", "passed/n_with_pass_judgment；不是 scored/planned。", "[0,1]", up},
	} {
		add(m.id, "运行状态", m.name, m.def, map[string]string{"scoring_coverage": "scored/planned", "pass_rate_on_scored": "passed/n_with_pass_judgment"}[m.id], m.span, m.dir, "报告计数/比例（非跨运行平均）", nil, []string{"all"})
	}
	for _, phase := range []string{"reset", "ingest", "ready", "query", "answer", "decision"} {
		add("timings."+phase+"_ms", "耗时", phase+" 耗时", "该阶段观测到的毫秒数分位值。", "p50/p95（线性插值）", "[0,+∞) ms", down, "对实际记录该阶段的样本求分位数", []string{"受宿主负载与并发影响，只在同机同期比较；不把阶段分位数相加当作总墙钟。"}, []string{"all"})
	}
	add("wall_seconds", "耗时", "墙钟秒数", "报告记录的运行持续时间。", "wall_seconds", "[0,+∞) s", down, "单次运行原值", []string{"受宿主负载、网络、并发及续跑影响。"}, []string{"all"})
	for _, role := range []string{"sut", "evaluator"} {
		for _, m := range []struct{ id, def string }{
			{"calls", "调用/尝试次数；SUT 为已到达上游的尝试，含重试；评测器含回放调用。"},
			{"prompt_tokens", "上游报告的输入 token 总数。"}, {"cached_tokens", "输入 token 中缓存命中的部分，不能与 prompt 再相加。"},
			{"completion_tokens", "上游报告的输出 token 总数。"}, {"reasoning_tokens", "输出 token 中推理部分，不能与 completion 再相加。"},
			{"retries", "透明重试次数。"}, {"rate_limited_responses", "上游 HTTP 429 次数。"}, {"circuit_opens", "断路器打开次数。"},
			{"unmetered_calls", "上游未提供 usage 的调用数，不等于零 token。"}, {"client_disconnects", "SUT 在排队/重试/执行时断开连接的次数。"},
			{"queue_wait_p95_ms", "治理队列等待时间第 95 百分位，毫秒。"}, {"http_429", "HTTP 429 尝试数。"}, {"http_4xx", "HTTP 4xx 尝试数（含 429）。"}, {"http_5xx", "HTTP 5xx 尝试数。"},
			{"cost_usd_new", "本次新增美元费用；未知为 null。"}, {"cost_usd_original", "含回放响应原始调用的美元费用；未知为 null。"}, {"cassette_hits", "命中本地回放的调用数。"},
		} {
			add("cost."+role+"."+m.id, "费用与调用", role+" "+m.id, m.def, "账本汇总原值", "[0,+∞) 或 null", down, "跨尝试累计；queue_wait_p95_ms 除外", []string{"SUT 与评测器流量分开；回放不产生新增费用；缺失用量不能当作 0。"}, []string{"all"})
		}
	}
	slices.SortFunc(out, func(a, b MetricDef) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func haluGlossary() []MetricDef {
	out := []MetricDef{}
	add := func(id, def, formula string, count bool, lower bool) {
		span := "[0,1]"
		direction := Bool(!lower)
		agg := "官方聚合记录的比值；summary/by_category 的同名值为定义用例均值"
		if count {
			span = "[0,+∞)"
			direction = nil
			agg = "官方聚合记录计数/权重和"
		}
		out = append(out, MetricDef{ID: "halumem." + id, Group: "HaluMem", NameZh: strings.TrimSuffix(def, "。"), DefinitionZh: def, Formula: formula, Range: span, HigherIsBetter: direction, Aggregation: agg, Caveats: []string{"all 分母包含无效裁判记录；valid 排除 None/非法标签；零分母输出 null。", "空记忆点检查点不评分；完整 dump 审计可能产生大量裁判调用。"}, AppliesTo: []string{"halumem"}})
	}
	for _, den := range []string{"all", "valid"} {
		add("memory_integrity.recall("+den+")", "非 interference 记忆点中完整覆盖（score=2）的比例。", "count(score=2)/非干扰点分母", false, false)
		add("memory_integrity.weighted_recall("+den+")", "按 importance 加权的记忆完整度。", "Σ(score/2 × importance)/Σ importance", false, false)
		add("memory_accuracy.interference_accuracy("+den+")", "interference 点被正确排除的比例。", "count(score=0)/interference 点分母", false, false)
		add("memory_accuracy.target_accuracy("+den+")", "标记包含于 gold 的新增记忆的平均准确度。", "Σ(score/2)/included_in_gold 分母", false, false)
		add("memory_accuracy.weighted_accuracy("+den+")", "全部新增记忆的平均准确度。", "Σ(score/2)/新增记忆分母", false, false)
	}
	for _, prefix := range []string{"memory_integrity", "memory_accuracy"} {
		for _, id := range []string{"memory_num", "memory_valid_num"} {
			add(prefix+"."+id, "全部/有效记忆记录数。", "count(records)", true, false)
		}
	}
	for _, id := range []string{"memory_importance_sum", "memory_valid_importance_sum"} {
		add("memory_integrity."+id, "非干扰记忆点的全部/有效 importance 总和。", "Σ importance", true, false)
	}
	for _, id := range []string{"target_memory_num", "target_memory_valid_num", "interference_memory_num", "interference_memory_valid_num"} {
		add("memory_accuracy."+id, "target/interference 全部或有效记录数。", "count(records)", true, false)
	}
	add("memory_extraction_f1", "target_accuracy(all) 与 integrity recall(all) 的调和均值。", "2PR/(P+R)", false, false)
	for _, group := range []struct {
		prefix, unit string
		labels       []string
	}{{"memory_update", "update_memory", []string{"correct", "hallucination", "omission", "other"}}, {"question_answering", "qa", []string{"correct", "hallucination", "omission"}}} {
		for _, label := range group.labels {
			for _, den := range []string{"all", "valid"} {
				add(group.prefix+"."+label+"_"+group.unit+"_ratio("+den+")", "裁判标签 "+label+" 在该组的比例。", "count(label)/对应分母", false, label != "correct")
			}
		}
		for _, suffix := range []string{"_num", "_valid_num"} {
			add(group.prefix+"."+group.unit+suffix, "该组全部/有效裁判记录数。", "count(records)", true, false)
		}
	}
	for _, typ := range []string{"Persona Memory", "Relationship Memory", "Event Memory", "*"} {
		for _, id := range []string{"memory_integrity_acc", "memory_update_acc", "memory_acc", "total_num"} {
			add("memory_type_accuracy."+typ+"."+id, "按记忆类型汇总完整覆盖/正确更新。", "integrity_hits/total、update_hits/total、(两者之和)/total 或 total", id == "total_num", false)
		}
	}
	add("judge_unfenced_json", "裁判响应使用无围栏 JSON 回退解析的次数。", "count(unfenced)", true, false)
	return out
}

// MetricMatches accepts numeric @k expansions and explicitly declared variable
// group suffixes, never an arbitrary unknown metric under a known benchmark.
func MetricMatches(id, pattern string) bool {
	if id == pattern {
		return true
	}
	if strings.HasSuffix(pattern, "@k") {
		prefix := strings.TrimSuffix(pattern, "k")
		tail, ok := strings.CutPrefix(id, prefix)
		if !ok || tail == "" {
			return false
		}
		for _, r := range tail {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if before, after, ok := strings.Cut(pattern, "*"); ok {
		return strings.HasPrefix(id, before) && strings.HasSuffix(id, after) && len(id) > len(before)+len(after)
	}
	return false
}
