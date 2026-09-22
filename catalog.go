package main

// The catalog is the progression content for the adaptive engine. Difficulty
// is intentionally approximate: selection combines it with mastery, retention,
// prerequisites, and the learner's recent success rather than using it as a
// fixed level gate.
type patternDefinition struct {
	id, expression, intent, skill string
	difficulty                    float64
	seeds                         []exerciseSeed
}

func catalogSeed(prompt, context string) exerciseSeed {
	return exerciseSeed{Prompt: prompt, Context: context}
}

func patternCatalog() []patternDefinition {
	return []patternDefinition{
		// Existing core patterns.
		{id: "going-to", expression: "was/were going to", intent: "plans", skill: "plans", difficulty: 2.2},
		{id: "modal-possibility", expression: "might / may", intent: "possibility", skill: "plans", difficulty: 3.0},
		{id: "conditional", expression: "if ... would", intent: "condition", skill: "conditionals", difficulty: 4.5},
		{id: "polite-request", expression: "Could you ...?", intent: "request", skill: "polite_request", difficulty: 3.2},
		{id: "polite-refusal", expression: "I don't think I'll be able to ...", intent: "refusal", skill: "polite_refusal", difficulty: 4.1},
		{id: "because", expression: "because / so", intent: "explanation", skill: "because", difficulty: 2.8},
		{id: "past-perfect", expression: "had already ...", intent: "plans", skill: "past_perfect", difficulty: 5.2},
		{id: "wish-past", expression: "I wish I had ...", intent: "explanation", skill: "past_perfect", difficulty: 5.8},

		// Child and beginner foundations.
		{id: "be-basic", expression: "I am ...", intent: "identity", skill: "foundations", difficulty: 1.0, seeds: []exerciseSeed{
			catalogSeed("我是一名学生。", "school / identity"), catalogSeed("我今天很累。", "feelings / today"),
		}},
		{id: "have-basic", expression: "I have ...", intent: "possessions", skill: "foundations", difficulty: 1.1, seeds: []exerciseSeed{
			catalogSeed("我有一只小狗。", "pet / home"), catalogSeed("我今天有一个问题。", "question / school"),
		}},
		{id: "like", expression: "I like ...", intent: "preferences", skill: "preferences", difficulty: 1.2, seeds: []exerciseSeed{
			catalogSeed("我喜欢画画。", "hobby / children"), catalogSeed("我喜欢吃苹果。", "food / preference"),
		}},
		{id: "can", expression: "I can ...", intent: "ability", skill: "ability", difficulty: 1.3, seeds: []exerciseSeed{
			catalogSeed("我会骑自行车。", "ability / transport"), catalogSeed("我能帮你收拾桌子。", "help / home"),
		}},
		{id: "want", expression: "I want to ...", intent: "wants", skill: "preferences", difficulty: 1.4, seeds: []exerciseSeed{
			catalogSeed("我想喝一杯水。", "drink / need"), catalogSeed("我想和朋友一起玩。", "friends / activity"),
		}},
		{id: "need", expression: "I need to ...", intent: "needs", skill: "daily_life", difficulty: 1.5, seeds: []exerciseSeed{
			catalogSeed("我需要做作业。", "homework / school"), catalogSeed("我需要早点睡觉。", "sleep / routine"),
		}},
		{id: "there-is", expression: "There is / There are ...", intent: "description", skill: "foundations", difficulty: 1.6, seeds: []exerciseSeed{
			catalogSeed("桌子上有一本书。", "classroom / object"), catalogSeed("公园里有很多孩子。", "park / children"),
		}},
		{id: "do-you", expression: "Do you ...?", intent: "questions", skill: "questions", difficulty: 1.8, seeds: []exerciseSeed{
			catalogSeed("你喜欢足球吗？", "sport / friends"), catalogSeed("你每天走路上学吗？", "school / routine"),
		}},

		// Everyday conversation.
		{id: "present-continuous", expression: "I'm ...-ing", intent: "current_activity", skill: "daily_life", difficulty: 2.0, seeds: []exerciseSeed{
			catalogSeed("我正在准备早餐。", "breakfast / morning"), catalogSeed("她正在给朋友打电话。", "phone / friends"),
		}},
		{id: "simple-past", expression: "I ... yesterday", intent: "past_event", skill: "past_tense", difficulty: 2.1, seeds: []exerciseSeed{
			catalogSeed("我昨天看了一部电影。", "movie / yesterday"), catalogSeed("我们上周去了海边。", "travel / last week"),
		}},
		{id: "future-will", expression: "I'll ...", intent: "future_plan", skill: "plans", difficulty: 2.2, seeds: []exerciseSeed{
			catalogSeed("我稍后会给你发消息。", "message / later"), catalogSeed("我明天会早点到。", "arrival / tomorrow"),
		}},
		{id: "would-like", expression: "I'd like ...", intent: "orders", skill: "polite_request", difficulty: 2.4, seeds: []exerciseSeed{
			catalogSeed("我想要一杯热茶。", "cafe / order"), catalogSeed("我想预订一张靠窗的桌子。", "restaurant / booking"),
		}},
		{id: "can-i", expression: "Can I ...?", intent: "permission", skill: "polite_request", difficulty: 2.5, seeds: []exerciseSeed{
			catalogSeed("我可以坐这里吗？", "public place / seat"), catalogSeed("我可以借用你的充电器吗？", "office / borrow"),
		}},
		{id: "should", expression: "You should ...", intent: "advice", skill: "advice", difficulty: 2.6, seeds: []exerciseSeed{
			catalogSeed("你应该多喝水。", "health / advice"), catalogSeed("你应该早点告诉老师。", "school / advice"),
		}},
		{id: "have-to", expression: "I have to ...", intent: "obligation", skill: "obligation", difficulty: 2.9, seeds: []exerciseSeed{
			catalogSeed("我今天必须加班。", "work / obligation"), catalogSeed("我得先接孩子回家。", "family / obligation"),
		}},
		{id: "present-perfect", expression: "I've ... before", intent: "experience", skill: "experience", difficulty: 3.1, seeds: []exerciseSeed{
			catalogSeed("我以前去过上海。", "travel / experience"), catalogSeed("我以前没有做过这种菜。", "cooking / experience"),
		}},
		{id: "would-you-mind", expression: "Would you mind ...?", intent: "polite_request", skill: "polite_request", difficulty: 3.4, seeds: []exerciseSeed{
			catalogSeed("你介意把声音调小一点吗？", "home / request"), catalogSeed("你介意稍后给我回电话吗？", "phone / request"),
		}},
		{id: "lets", expression: "Let's ...", intent: "suggestion", skill: "suggestions", difficulty: 3.0, seeds: []exerciseSeed{
			catalogSeed("我们先吃午饭吧。", "friends / lunch"), catalogSeed("我们周末去看展览吧。", "weekend / culture"),
		}},
		{id: "used-to", expression: "I used to ...", intent: "past_habit", skill: "past_tense", difficulty: 3.6, seeds: []exerciseSeed{
			catalogSeed("我以前每天坐公交车上学。", "school / past routine"), catalogSeed("她以前住在海边。", "home / past life"),
		}},
		{id: "comparative", expression: "... is more ... than ...", intent: "comparison", skill: "comparison", difficulty: 3.8, seeds: []exerciseSeed{
			catalogSeed("坐火车比坐飞机更舒服。", "travel / comparison"), catalogSeed("这个方案比上一个更简单。", "work / comparison"),
		}},

		// Intermediate and adult conversation.
		{id: "if-first", expression: "If ..., I'll ...", intent: "condition", skill: "conditionals", difficulty: 4.0, seeds: []exerciseSeed{
			catalogSeed("如果明天不下雨，我就去跑步。", "weather / health"), catalogSeed("如果你需要帮助，我就过来。", "help / friends"),
		}},
		{id: "unless", expression: "Unless ..., ...", intent: "condition", skill: "conditionals", difficulty: 4.3, seeds: []exerciseSeed{
			catalogSeed("除非你确认，否则我不会预订。", "booking / decision"), catalogSeed("除非交通好转，否则我们会迟到。", "traffic / arrival"),
		}},
		{id: "although", expression: "Although ..., ...", intent: "contrast", skill: "contrast", difficulty: 4.2, seeds: []exerciseSeed{
			catalogSeed("虽然很累，但我还是完成了工作。", "work / effort"), catalogSeed("虽然下着雨，我们还是出门了。", "weather / plans"),
		}},
		{id: "reported-speech", expression: "She said that ...", intent: "reporting", skill: "reporting", difficulty: 4.8, seeds: []exerciseSeed{
			catalogSeed("她说她明天会参加会议。", "work / report"), catalogSeed("老师说考试会推迟。", "school / report"),
		}},
		{id: "passive", expression: "It was ... by ...", intent: "description", skill: "passive", difficulty: 5.0, seeds: []exerciseSeed{
			catalogSeed("这座桥是由当地工程师设计的。", "city / design"), catalogSeed("这封邮件是昨天发出的。", "work / email"),
		}},
		{id: "could-have", expression: "I could have ...", intent: "regret", skill: "perfect_modals", difficulty: 5.2, seeds: []exerciseSeed{
			catalogSeed("我本来可以早点告诉你的。", "regret / communication"), catalogSeed("我们本来可以坐早班车。", "travel / regret"),
		}},
		{id: "should-have", expression: "I should have ...", intent: "regret", skill: "perfect_modals", difficulty: 5.2, seeds: []exerciseSeed{
			catalogSeed("我本应该先检查地址。", "mistake / delivery"), catalogSeed("我本应该带一把伞。", "weather / regret"),
		}},
		{id: "would-rather", expression: "I'd rather ...", intent: "preference", skill: "preferences", difficulty: 4.5, seeds: []exerciseSeed{
			catalogSeed("我宁愿明早再讨论这个问题。", "work / preference"), catalogSeed("我宁愿在家吃晚饭。", "home / preference"),
		}},
		{id: "professional-suggestion", expression: "I'd like to suggest ...", intent: "meeting", skill: "professional", difficulty: 5.0, seeds: []exerciseSeed{
			catalogSeed("我想建议我们先测试这个方案。", "work meeting / proposal"), catalogSeed("我想建议把截止日期推迟一天。", "work / deadline"),
		}},
		{id: "clarification", expression: "What I mean is ...", intent: "clarification", skill: "professional", difficulty: 4.7, seeds: []exerciseSeed{
			catalogSeed("我的意思是我们需要更多时间。", "work / clarification"), catalogSeed("我的意思是现在不适合做决定。", "discussion / clarification"),
		}},
		{id: "disagreement", expression: "I see your point, but ...", intent: "disagreement", skill: "professional", difficulty: 5.0, seeds: []exerciseSeed{
			catalogSeed("我明白你的观点，但这个方案成本太高。", "work / disagreement"), catalogSeed("我理解你的想法，但我们还需要更多证据。", "discussion / disagreement"),
		}},
		{id: "negotiate", expression: "Would it be possible to ...?", intent: "negotiation", skill: "professional", difficulty: 5.4, seeds: []exerciseSeed{
			catalogSeed("可以把面试改到下周吗？", "interview / reschedule"), catalogSeed("可以请你再考虑一下价格吗？", "shopping / negotiation"),
		}},
		{id: "hedge", expression: "It seems that ...", intent: "opinion", skill: "nuance", difficulty: 5.2, seeds: []exerciseSeed{
			catalogSeed("看来项目进度比预期慢。", "work / observation"), catalogSeed("看来他们已经做出决定了。", "discussion / observation"),
		}},
		{id: "mixed-conditional", expression: "If I had ..., I would ...", intent: "condition", skill: "conditionals", difficulty: 6.2, seeds: []exerciseSeed{
			catalogSeed("如果我当时知道，我现在就不会这么担心了。", "regret / present result"), catalogSeed("如果我们早点准备，现在就不会这么忙了。", "work / planning"),
		}},
		{id: "having-said-that", expression: "Having said that, ...", intent: "contrast", skill: "nuance", difficulty: 6.0, seeds: []exerciseSeed{
			catalogSeed("话虽如此，我还是认为这个方案值得尝试。", "work / balanced opinion"), catalogSeed("话虽如此，我们也不能忽视成本。", "discussion / qualification"),
		}},
		{id: "formal-opinion", expression: "I'm not entirely convinced that ...", intent: "opinion", skill: "nuance", difficulty: 6.5, seeds: []exerciseSeed{
			catalogSeed("我不完全相信这个方法能解决问题。", "work / cautious disagreement"), catalogSeed("我不太确信现在是改变计划的好时机。", "planning / cautious opinion"),
		}},
	}
}

func catalogScenes() [][3]string {
	return [][3]string{
		{"school", "School and Classroom", "School conversations and classroom routines"},
		{"family", "Family and Home", "Home, family, and childcare situations"},
		{"shopping", "Shopping and Services", "Shopping, prices, returns, and service"},
		{"health", "Health and Wellbeing", "Appointments, symptoms, and everyday wellbeing"},
		{"airport", "Airport and Travel", "Travel, transport, and hotel conversations"},
		{"interview", "Interview and Career", "Job interviews and career conversations"},
		{"presentation", "Presentation and Discussion", "Explaining ideas to a group"},
	}
}

func catalogIntents() [][2]string {
	return [][2]string{
		{"identity", "Identity"}, {"possessions", "Possessions"}, {"preferences", "Preferences"},
		{"ability", "Ability"}, {"wants", "Wants"}, {"needs", "Needs"}, {"description", "Description"},
		{"questions", "Questions"}, {"current_activity", "Current activity"}, {"past_event", "Past event"},
		{"future_plan", "Future plan"}, {"orders", "Orders"}, {"permission", "Permission"}, {"advice", "Advice"},
		{"obligation", "Obligation"}, {"experience", "Experience"}, {"polite_request", "Polite request"},
		{"suggestion", "Suggestion"}, {"past_habit", "Past habits"}, {"comparison", "Comparison"},
		{"contrast", "Contrast"}, {"reporting", "Reported information"}, {"regret", "Regret"},
		{"meeting", "Meeting"}, {"clarification", "Clarification"}, {"disagreement", "Disagreement"},
		{"negotiation", "Negotiation"}, {"opinion", "Opinion"},
	}
}

func assessmentPatternIDs() []string {
	// This is a deliberately non-catalog-order route: stable early anchors
	// establish a baseline, then probes span daily, intermediate, professional,
	// and nuance bands. The selector can confirm the boundary without replaying
	// every one of the 44 patterns.
	return []string{"going-to", "modal-possibility", "because", "simple-past", "present-perfect", "if-first", "reported-speech", "professional-suggestion", "negotiate", "having-said-that", "formal-opinion", "mixed-conditional"}
}
