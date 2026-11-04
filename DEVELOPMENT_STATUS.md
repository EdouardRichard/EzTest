# Development Status

EzTest 0.2.1 — release readiness in progress (S0-S10 retry14 PASS; final-HEAD trap re-run + security history pending).
# EzTest 寮€鍙戠姸鎬侊紙0.2 / P8锛?

> 鐘舵€侊細**P8 宸蹭弗鏍肩鏍稿畬鎴愶紙S9/S10 琛ヨ窇 PASS锛汱16鈥揕18 宸插叧闂級**锛?026-11-02锛涘彂甯冨垎鏀?`main`/`dev` 淇濈暀 B 鏃剁偣锛屽綋鍓?`feat/v0.2` 缁х画 A 琛ュ己锛夈€?
> 浜у搧瀹氫綅锛欰I 椹卞姩銆佸崐鑷富銆佺鍒扮娴嬭瘯绯荤粺锛?.2 涓哄畬鏁存浛浠ｇ増锛?.1 P0鈥揚8 鍏ㄥ姛鑳藉榻?+ P9 浜旈」锛夈€?
> 璁捐鏉冨▉锛歔docs/version-V0.2/README.md](docs/version-V0.2/README.md) 涓?
> [docs/version-V0.2/contracts/README.md](docs/version-V0.2/contracts/README.md)銆?
> 0.1 闀垮巻鍙插凡褰掓。鍒?[docs/version-V0.1/DEVELOPMENT_STATUS.md](docs/version-V0.1/DEVELOPMENT_STATUS.md)銆?

## 0. 楠岃瘉鐘舵€侊紙鍏堣锛?

- **P8 宸蹭弗鏍肩鏍稿畬鎴?*锛氳瘉鎹储寮?[docs/version-V0.2/acceptance/p8/](docs/version-V0.2/acceptance/p8/)銆?
  鍏ㄦ梾绋?S0鈥揝6锛?20s锛? 杞緞娓咃級銆丼10銆佸鍓湰 S8锛?2 PASS/0 FAIL锛夈€?7 琛岃兘鍔涚煩闃?
  锛?8 PASS/9 PASS(prior)/0 FAIL锛夈€佹祻瑙堝櫒 E2E锛?1 passed/0 skipped锛?4 寮犳埅鍥撅級銆侀潤鎬侀棬 18/18 鍧囬€氳繃銆?
  **A 琛ヨ窇 S9/S10锛歟xit 0锛孲9 `pinned_facts=4` 涓?packet 淇濈暀锛孲10 12 鐢ㄤ緥/2 娉?鏆傚仠-妫€鏌ョ偣/`/p8/large` head+ref/鏃犻噸澶嶆楠わ紝12/12 passed锛汱16鈥揕18 宸插叧闂€?* 璇佹嵁涓庨檺鍒惰
  [phase-08-handoff](docs/version-V0.2/phases/phase-08-handoff.md)銆?
- **宸茶褰曡瘉鎹?*锛歅6锛坄acceptance/p6/`锛岀湡瀹?LLM + 鐪熷疄 Chrome锛孲0鈥揝9 PASS锛? 寮犳埅鍥撅級涓?P7
  锛坄acceptance/p7/`锛?3/13 鍦烘櫙銆?56 checks銆?1 寮?E2E 鎴浘锛岃鐩?P9 浜旈」锛夈€?
- **Docker 鏈疄璺?*锛歚deploy/` 鐨?Compose/Dockerfile 宸蹭氦浠樺苟闈欐€佹牎楠岋紱鏈満鏈畨瑁?Docker锛屼粠鏈瀯寤?
  鎴栧惎鍔ㄣ€?
- **SLO 涓庡畨鍏ㄥ璁℃寜鐢ㄦ埛瑁佸畾璺宠繃**锛圥8-D-03 / P8-D-04锛夛細涓嶄骇鍑?SLO 鎶ュ憡銆佷笉杩愯 0.1
  `security-audit.ps1`锛屾枃妗ｄ笉浣滅浉鍏冲绉般€?
- P8 鐨?fake-LLM 纭畾鎬у洖褰掍笌鎬ц兘鍩哄噯鎸夌敤鎴疯瀹氫笉鍋氾紙P8-D-01锛夈€?

## 1. P0鈥揚8 闃舵

| 闃舵 | 涓婚 | 涓昏浜や粯 | 璇佹嵁 |
|---|---|---|---|
| P0 | 骞冲彴搴曞骇涓庡绾?| 4 椤瑰惎鍔?env銆佽缃腑蹇冦€佷簨浠?瀹¤/SSE銆佸唴瀹瑰瓨鍌ㄣ€佺粺涓€淇″皝/閿欒鐮併€佽縼绉讳笌 OpenAPI 楠ㄦ灦 | [phase-00-handoff](docs/version-V0.2/phases/phase-00-handoff.md) |
| P1 | 鎺у埗闈?+ 婢勬竻 | 浠诲姟鏉?reconciler/绉熺害銆佸洟闃熻繍琛屾椂銆佷笂涓嬫枃鍖呫€佹緞娓呭璇濅笌闇€姹傜増鏈寲銆佷汉鏈虹‘璁?| [phase-01-handoff](docs/version-V0.2/phases/phase-01-handoff.md) |
| P2 | 璁捐 | 璁捐瀵硅瘽銆佸満鏅?鐢ㄤ緥/姝ラ/鏂█銆佺敤渚嬬増鏈笌纭銆佽剳鍥句笌鑷銆佹暟鎹墠缃?| [phase-02-handoff](docs/version-V0.2/phases/phase-02-handoff.md) |
| P3 | 鎵ц | 鎵ц鎰忓浘/娉㈡銆丳laywright 鎵ц鍣ㄤ笌鍗忚銆佸 SUT 涓婁笅鏂囥€佽瘉鎹€佹殏鍋?鎭㈠銆佷汉鏈鸿姹?| [phase-03-handoff](docs/version-V0.2/phases/phase-03-handoff.md) |
| P4 | 浜や粯 + 閾捐矾 | 闂鐢熷懡鍛ㄦ湡銆佹姤鍛?revision/HTML銆佹帰绱㈤摼璺€侀渶姹傞棴鐜€佺増鏈噸寮€ | [phase-04-handoff](docs/version-V0.2/phases/phase-04-handoff.md) |
| P5 | 浠ｇ爜鏅鸿兘 + 瑙嗚鏂█ | 鍙鏈湴 Git 绱㈠紩/鏍?diff/褰卞搷/閿氱偣/浜嬪疄澶嶆牳銆佽瑙夋柇瑷€涓?diff 璇佹嵁銆佽川閲忔寚鏍?| [phase-05-handoff](docs/version-V0.2/phases/phase-05-handoff.md) |
| P6 | 鍏ㄥ姛鑳借ˉ榻?| 鍏ㄥ眬鎼滅储/寰呭姙銆佽川閲忎腑蹇?璇勬祴銆丄gent 涓績銆佸叏灞€闂/鎶ュ憡銆侀摼璺?v2銆佹彁绀鸿瘝鐗堟湰鍖?| [phase-06-handoff](docs/version-V0.2/phases/phase-06-handoff.md) |
| P7 | P9 浜旈」 | 鎶ュ憡 HTML/PDF 涓庢ā鏉裤€佽法鐗堟湰鐭ヨ瘑銆佺粺涓€ GC銆佸瀹炰緥/婊氬姩鍗囩骇銆侀棶棰橀噸寮€/鍚堝苟/璺ㄦ尝娆″幓閲?| [phase-07-handoff](docs/version-V0.2/phases/phase-07-handoff.md)銆乕acceptance/p7](docs/version-V0.2/acceptance/p7/) |
| P8 | 纭寲涓庨獙鏀?| H1鈥揌11 纭寲锛堣法瀹炰緥璁剧疆澶辨晥銆乮n-flight 璁℃暟銆佺煡璇?FTS銆乸er-executor 娲惧彂銆丳DF 涓婇檺銆佺増鏈覆銆佸姩鎬?E2E seed銆侀洩鑺?瀹炰緥 ID銆丆I 鍘?0.1 鍖栥€佷簨浠?瀹¤淇濈暀銆侀檺娴侊級銆丳8 濂戠害鍐荤粨銆佸叏閲忛獙鏀躲€佹枃妗?0.2 鍖?| [phase-08-handoff](docs/version-V0.2/phases/phase-08-handoff.md)銆乕acceptance/p8](docs/version-V0.2/acceptance/p8/) |

## 2. 濂戠害涓庤妯¤鏁帮紙P8 鍐荤粨锛?

| 椤?| 璁℃暟 |
|---|---|
| OpenAPI | `0.2.0-p8` / `x-frozen: P8`锛?*162 paths / 200 operations / 264 schemas** |
| 涓氬姟琛?| **85**锛圥8 涓嶆柊澧炶〃锛?|
| 杩佺Щ | **42**锛圥8 杩藉姞 #41 `api_instance.active_requests/active_work`銆?42 `case_run_sut_context.executor_id`锛?|
| 鏁版嵁搴撹缃?| **85**锛圥8 鏂板 `security.rate_limit_per_minute=0`銆乣retention.event_days=90`锛?|
| 浜嬩欢绫诲瀷 | **118**锛圥8 涓嶆柊澧炵被鍨嬶紱鏂板瀹¤ action `retention.cleanup`锛?|
| 浠诲姟绫诲瀷 | **19** |
| Agent profile | **14** |
| 闄愮晫涓婁笅鏂?| **20** |
| 鎵ц鍣ㄧ嚎鍗忚 | **v1.2**锛圥8 鍙敼娲惧彂璺敱锛屾棤 action 鍙樺寲锛?|

濂戠害闂ㄧ锛歚node scripts/p8-contract-check.cjs`锛圥2鈥揚7 妫€鏌ュ凡鏀逛负鍓嶅悜鍏煎锛夈€?

## 3. 娴嬭瘯闈?

| 椤?| 璁℃暟/鐘舵€?|
|---|---|
| Go 娴嬭瘯 | **321 涓?`*_test.go` / 103 涓寘**锛圥8 璧风偣 316/102锛汬1鈥揌11 鏂板娴嬭瘯闅忓垎鏀姞鍏ワ級 |
| 鍓嶇 Vitest | **105 涓祴璇曟枃浠?* |
| executor / renderer | 111 / 23锛圥7 鏀跺彛璁板綍锛汸8 鏈敼鍏跺崗璁笌瀹炵幇锛?|
| 缁熶竴娴嬭瘯鍏ュ彛 | `pwsh scripts/test.ps1`锛圙o unit + integration銆佸墠绔?lint/typecheck/test锛?|
| 濂戠害妫€鏌?| `node scripts/p8-contract-check.cjs`锛汸2鈥揚7 濂戠害鑴氭湰鍓嶅悜鍏煎 |
| 闈欐€佹鏌?| `scripts/p8-static-check.ps1`锛圥8 鏀跺彛锛涙牎楠?4 env銆佹枃妗ｃ€佸绾﹁鏁般€丆ompose銆佹棤 0.1 闂ㄧ寮曠敤銆佹棤 secret锛?|

## 4. 楠屾敹璇佹嵁

| 闃舵 | 璇佹嵁璺緞 | 鎽樿 |
|---|---|---|
| P6 | [docs/version-V0.2/acceptance/p6/](docs/version-V0.2/acceptance/p6/) | S0鈥揝9 PASS锛涜川閲?鎼滅储/寰呭姙/Agent 涓績/鍏ㄥ眬涓績/閾捐矾 v2锛涚湡瀹?LLM 璇勬祴锛? 寮犳埅鍥?|
| P7 | [docs/version-V0.2/acceptance/p7/](docs/version-V0.2/acceptance/p7/) | S0鈥揝12 鍏?13 鍦烘櫙銆?56 checks PASS锛汸DF/鐭ヨ瘑/GC/澶氬壇鏈?SSE/绂荤嚎閲嶆淳/闂澶嶅彂锛?/5 E2E銆?1 寮犳埅鍥?|
| P8 | [docs/version-V0.2/acceptance/p8/](docs/version-V0.2/acceptance/p8/) | 鍏ㄦ梾绋?闊ф€?澶氬壇鏈?鑳藉姏鐭╅樀/E2E/闈欐€侀棬 PASS锛涢檺鍒惰 handoff/known-limitations |

P8 楠屾敹鍏ュ彛锛堣瑙?runbook锛夛細`p7-acceptance.ps1`锛圥9 鍥炲綊锛夆啋 鍏嬮殕 `eztest_v02_p7` 涓?
`eztest_v02_p8` 鈫?`p8-acceptance.ps1`锛圫0鈥揝6锛夆啋 `p8-resilience.ps1`锛圫9鈥揝10锛夆啋 `p8-multi.ps1`
锛圫8锛夆啋 `p8-capability.ps1`锛圫11锛夆啋 `p8-e2e.ps1`锛圫12锛夆啋 `p8-static-check.ps1`锛圫13锛夈€傜幆澧冧负鐪熷疄
LLM銆佺湡瀹?Chrome銆佸弻 SUT銆佸弻 API 鍓湰涓庡叡浜唴瀹圭洰褰曘€?

## 5. 宸茬煡闄愬埗

1. **P8 楠屾敹宸插畬鎴?*锛氬叏鏃呯▼/闊ф€?澶氬壇鏈?鑳藉姏鐭╅樀/E2E/闈欐€侀棬 PASS锛涜瘉鎹
   [acceptance/p8/](docs/version-V0.2/acceptance/p8/) 涓?[phase-08-handoff](docs/version-V0.2/phases/phase-08-handoff.md)銆?
2. **Docker 鏈疄璺?*锛欳ompose/Dockerfile 鍙仛闈欐€佹牎楠岋紙`frontend/scripts/validate-compose.mjs`锛夛紱
   棣栨鍦ㄧ湡瀹?Docker 涓绘満鎵ц鍓嶉渶澶嶆牳缃戠粶銆佸嵎銆乺enderer URL/token 涓庨暅鍍忔瀯寤恒€?
3. **SLO 鏈祴閲忋€佸畨鍏ㄥ璁℃湭鎵ц**锛氭寜鐢ㄦ埛瑁佸畾璺宠繃锛涗笉鎵胯 SLO 闃堝€兼垨鐢熶骇绾у畨鍏ㄥ姞鍥恒€?
4. **澶氬壇鏈竟鐣?*锛欰PI 澶氬壇鏈€氳繃 PostgreSQL 绉熺害/advisory lock 鍗忚皟锛岃姹傛墍鏈夊壇鏈叡浜悓涓€鍐呭
   鏂囦欢绯荤粺锛涗笉鍋氳嚜鍔ㄦ墿缂╁銆佽法鍖哄煙 HA 鎴?Redis 鍏变韩闄愭祦妗躲€傞檺娴佷负姣忓疄渚嬮搴︼紝鎬婚€熺巼 = 鍓湰鏁?脳
   闄愰锛堥粯璁?0 鍏抽棴锛夈€?
5. **鏁版嵁杩佺Щ**锛?.1 鏁版嵁涓嶅仛鍘熷湴杩佺Щ锛?.2 浣跨敤鏂板簱锛?.1 鏂囨。涓庤剼鏈粎褰掓。鍙傝€冦€?
6. **澶栭儴渚濊禆**锛氱湡瀹炴墽琛岄渶瑕佸彲鐢?Chrome/Chromium锛涚湡瀹?AI 璺緞闇€瑕?OpenAI 鍏煎 LLM锛汸DF 闇€瑕?
   `renderer/` 杩涚▼銆傛病鏈?LLM 鏃剁‘瀹氭€ц矾寰勪粛宸ヤ綔锛孉I 璺緞杩斿洖鍙噸璇曢敊璇€?
7. **淇濈暀涓?GC**锛氫簨浠朵繚鐣欓粯璁?90 澶┿€佸璁￠粯璁?90 澶╋紱GC 榛樿 dry-run锛宎pply 闇€鏄惧紡纭锛涙病鏈?
   鍐峰瓨鍌?褰掓。瀵煎嚭銆?
8. **楠屾敹鐜**锛氶獙鏀惰剼鏈互 Windows PowerShell 涓轰富锛屼娇鐢ㄦ湰鏈?PostgreSQL/Chrome锛涜法骞冲彴澶嶈窇闇€
   绛変环鐜銆?

## 6. 鏂囨。鍏ュ彛

- 浜у搧 README锛歔README.md](README.md) / [README.zh-CN.md](README.zh-CN.md)
- 0.2 璁捐绱㈠紩锛歔docs/version-V0.2/README.md](docs/version-V0.2/README.md)
- 0.2 濂戠害锛歔docs/version-V0.2/contracts/](docs/version-V0.2/contracts/)
- 閮ㄧ讲/Compose锛歔deploy/README.md](deploy/README.md)
- P8 runbook锛歔docs/version-V0.2/acceptance/p8/runbook.md](docs/version-V0.2/acceptance/p8/runbook.md)
- 0.1 褰掓。锛歔docs/version-V0.1/README.md](docs/version-V0.1/README.md)

## 7. 璁稿彲璇?

Apache License 2.0锛岃 [LICENSE](LICENSE)銆?
