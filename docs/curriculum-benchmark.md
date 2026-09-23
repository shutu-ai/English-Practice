# Common-Life Curriculum Benchmark

V2.4 adds an independent `common-life-v1` taxonomy for measuring whether the
exercise corpus covers real communication needs. The taxonomy is intentionally
not a copy of the production pattern list: it covers Daily Life,
Family & Friends, Shopping, Restaurant, Health, Travel, Hotel, Airport, Phone
Call, Social Conversation, Work, Meeting, and Problem Solving.

Each capability and high-frequency intent has a frequency weight. Reports
include raw capability coverage, weighted coverage, weighted high-frequency
coverage, scene/intent/pattern coverage, context diversity, and UNSEEN,
TOUCHED, COVERED, or WELL_COVERED depth. The independent curriculum judge
receives only the public Chinese prompt, reference answer, and optional known
scene; production pattern IDs, mastery, and coverage results are not inputs.

```powershell
go run . benchmark curriculum --samples 500 --seed 42 --format json --output .acceptance-data/v24-curriculum.json
```

The benchmark reports gaps and distribution bias. It does not add, remove, or
rebalance production curriculum content.
