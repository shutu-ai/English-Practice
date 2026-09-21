<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

type Exercise = { exercise_id: string; chinese_prompt: string; target_pattern: string; pattern_id: string; scene_id: string; difficulty: number }
type Evaluation = { verdict: string; meaning_score: number; grammar_score: number; naturalness_score: number; pattern_score: number; errors: Array<{type:string; severity:string; explanation:string}>; suggested_answer: string; explanation_zh: string }
type HistoryItem = { id:string; submitted_at:string; prompt:string; answer:string; verdict:string; suggested_answer:string; errors:Array<unknown>; pattern_id:string; scene_id:string }
type Provider = { id:string; name:string; type:string; base_url:string; api_key?:string; model:string; timeout:number; temperature:number; max_tokens:number; enabled:boolean }
type ReviewItem = { id:string; pattern:string; due_at:string; priority:number; reason:string }

const page = ref('practice')
const exercise = ref<Exercise | null>(null)
const answer = ref('')
const evaluation = ref<Evaluation | null>(null)
const loading = ref(false)
const message = ref('')
const failedAttemptId = ref('')
const mode = ref('adaptive')
const history = ref<HistoryItem[]>([])
const progress = ref<Record<string, any>>({})
const scenes = ref<Array<{id:string;name:string}>>([])
const selectedScene = ref('')
const reviews = ref<ReviewItem[]>([])
const providers = ref<Provider[]>([])
const provider = ref<Provider>({id:'',name:'Local provider',type:'openai-compatible',base_url:'',api_key:'',model:'',timeout:45,temperature:.2,max_tokens:800,enabled:false})

async function api<T>(url:string, init?:RequestInit):Promise<T>{
  const response = await fetch(url, {headers:{'Content-Type':'application/json'}, ...init})
  const data = await response.json()
  if(!response.ok) throw new Error(data.error || '请求失败')
  return data
}
async function nextExercise(){ loading.value=true; message.value=''; evaluation.value=null; answer.value=''; try{exercise.value=await api<Exercise>('/api/practice/next',{method:'POST',body:JSON.stringify({mode:mode.value,scene_id:selectedScene.value})})}catch(e){message.value=(e as Error).message}finally{loading.value=false} }
async function submit(){ if(!exercise.value || !answer.value.trim()) return; loading.value=true; message.value=''; failedAttemptId.value=''; try{const result=await api<{attempt_id:string;evaluation_status:string;evaluation?:Evaluation;error?:string}>('/api/attempts',{method:'POST',body:JSON.stringify({exercise_id:exercise.value.exercise_id,answer:answer.value})}); if(result.evaluation) evaluation.value=result.evaluation; else {failedAttemptId.value=result.attempt_id;message.value=result.error||'评估暂时失败，本次记录已保存，但不会影响掌握度。'}}catch(e){message.value=(e as Error).message}finally{loading.value=false} }
async function reevaluate(){ if(!failedAttemptId.value) return; loading.value=true; message.value=''; try{const result=await api<{evaluation_status:string;evaluation?:Evaluation;error?:string}>('/api/attempts/'+failedAttemptId.value+'/reevaluate',{method:'POST'}); if(result.evaluation){evaluation.value=result.evaluation;failedAttemptId.value=''}else{message.value=result.error||'重新评估仍然失败'}}catch(e){message.value=(e as Error).message}finally{loading.value=false} }
async function loadHistory(){history.value=await api<HistoryItem[]>('/api/history')}
async function loadProgress(){progress.value=await api('/api/progress')}
async function loadScenes(){scenes.value=await api('/api/scenes')}
async function loadReviews(){reviews.value=await api<ReviewItem[]>('/api/reviews')}
async function loadProviders(){providers.value=await api<Provider[]>('/api/providers');if(providers.value[0])provider.value={...provider.value,...providers.value[0]}}
async function saveProvider(){const hadKey=!!provider.value.api_key;const saved=await api<Provider>('/api/providers',{method:'POST',body:JSON.stringify(provider.value)});provider.value={...provider.value,...saved};await loadProviders();message.value=hadKey?'Provider 已保存。':'Provider 已保存，未填写 API Key，已保留已有 Key。'}
async function testProvider(){const result=await api<{ok:boolean;error?:string}>('/api/providers/test',{method:'POST',body:JSON.stringify(provider.value)});message.value=result.ok?'Connection successful.':(result.error||'Connection failed.')}
async function changePage(next:string){page.value=next;if(next==='history')await loadHistory();if(next==='progress')await loadProgress();if(next==='review')await loadReviews();if(next==='settings')await loadProviders()}
const verdictText=computed(()=>({correct:'表达正确',mostly_correct:'基本正确',needs_improvement:'需要改进',incorrect:'需要重写'}[evaluation.value?.verdict||'']||''))
onMounted(async()=>{await Promise.all([loadScenes(),nextExercise(),loadProviders()] )})
</script>

<template>
  <div class="shell">
    <header class="topbar"><div class="brand"><span class="brand-mark">文</span><div><strong>English Practice AI</strong><small>主动表达训练</small></div></div><nav><button v-for="item in [['practice','Practice'],['review','Review'],['scenes','Scenes'],['progress','Progress'],['history','History'],['settings','Settings']]" :key="item[0]" :class="{active:page===item[0]}" @click="changePage(item[0] as string)">{{item[1]}}</button></nav><div class="status-dot" title="本地运行"></div></header>
    <main>
      <section v-if="page==='practice'" class="practice-page">
        <div class="eyebrow">{{mode==='assessment'?'ADAPTIVE ASSESSMENT':'DAILY PRACTICE'}}</div>
        <div class="practice-head"><div><h1>把想法说成自然英语。</h1><p>先独立写出完整句子，再查看针对性的反馈。</p></div><div class="mode-switch"><button v-for="m in ['adaptive','weak','review']" :key="m" :class="{selected:mode===m}" @click="mode=m;nextExercise()">{{m==='adaptive'?'Adaptive':m==='weak'?'Weak Areas':'Review'}}</button></div></div>
        <div class="exercise-card" v-if="exercise"><div class="card-meta"><span>中文表达</span><span>难度 {{exercise.difficulty.toFixed(1)}} / 8</span></div><p class="prompt">{{exercise.chinese_prompt}}</p><div class="target"><span>目标句型</span><b>{{exercise.target_pattern}}</b></div><textarea v-model="answer" :disabled="!!evaluation" placeholder="输入完整的英文句子…" @keydown.ctrl.enter="submit"></textarea><div class="action-row"><span class="hint">Ctrl + Enter 提交</span><button class="primary" :disabled="loading||!!evaluation||!answer.trim()" @click="submit">{{loading?'评估中…':'Submit'}}</button></div></div>
        <div v-if="evaluation" class="feedback-card"><div class="feedback-title"><span class="verdict" :class="evaluation.verdict">{{verdictText}}</span><button class="next" @click="nextExercise">下一题 <span>→</span></button></div><div class="answer-compare"><div><label>你的表达</label><p>{{answer}}</p></div><div><label>更自然的表达</label><p class="suggested">{{evaluation.suggested_answer}}</p></div></div><p class="explanation">{{evaluation.explanation_zh}}</p><div class="scores"><div v-for="score in [['Meaning',evaluation.meaning_score],['Grammar',evaluation.grammar_score],['Naturalness',evaluation.naturalness_score],['Pattern',evaluation.pattern_score]]" :key="score[0]" class="score"><span>{{score[0]}}</span><div class="bar"><i :style="{width:`${Number(score[1])*100}%`}"></i></div><b>{{Math.round(Number(score[1])*100)}}%</b></div></div><div v-if="evaluation.errors.length" class="errors"><div v-for="error in evaluation.errors" :key="error.type" class="error"><span>{{error.type}}</span><p>{{error.explanation}}</p></div></div></div>
        <div v-if="message" class="notice-row"><p class="notice">{{message}}</p><button v-if="failedAttemptId" class="secondary" :disabled="loading" @click="reevaluate">重新评估</button></div>
      </section>
      <section v-else-if="page==='history'" class="content-page"><div class="eyebrow">PRACTICE HISTORY</div><h1>你的真实表达</h1><p class="lede">每一次尝试都保留在这里，方便观察句型和错误的变化。</p><div class="history-list"><article v-for="item in history" :key="item.id" class="history-item"><div class="history-date">{{new Date(item.submitted_at).toLocaleString()}}</div><div class="history-prompt">{{item.prompt}}</div><div class="history-answer">{{item.answer}}</div><span class="pill" :class="item.verdict">{{item.verdict}}</span><div class="history-suggestion">{{item.suggested_answer}}</div></article><p v-if="!history.length" class="empty">还没有练习记录。</p></div></section>
      <section v-else-if="page==='progress'" class="content-page"><div class="eyebrow">PROGRESS DASHBOARD</div><h1>知道下一步练什么。</h1><p class="lede">系统根据长期练习记录，持续找到你还不熟悉的表达。</p><div class="stats"><div><span>近期练习</span><strong>{{progress.recent_attempts||0}}</strong></div><div><span>近期成功率</span><strong>{{Math.round((progress.recent_success_rate||0)*100)}}%</strong></div><div><span>Global Difficulty</span><strong>{{(progress.global_difficulty||3).toFixed(1)}}</strong></div></div><h2>需要更多练习的句型</h2><div class="weak-list"><div v-for="item in progress.weak_patterns||[]" :key="item.pattern"><span>{{item.pattern}}</span><div class="bar"><i :style="{width:`${item.mastery*100}%`}"></i></div><b>{{Math.round(item.mastery*100)}}%</b></div></div></section>
      <section v-else-if="page==='scenes'" class="content-page"><div class="eyebrow">SCENES</div><h1>在真实场景里练习。</h1><p class="lede">选择一个场景，系统会保持句型目标，同时替换中文表达。</p><div class="scene-grid"><button v-for="scene in scenes" :key="scene.id" @click="selectedScene=scene.id;changePage('practice');nextExercise()"><strong>{{scene.name}}</strong><span>开始练习 →</span></button></div></section>
      <section v-else-if="page==='review'" class="content-page"><div class="eyebrow">REVIEW SCHEDULER</div><h1>把需要复习的句型放在合适的时间。</h1><p class="lede">复习优先级来自最近错误、严重程度、掌握度和长期保留。</p><div class="history-list"><article v-for="item in reviews" :key="item.id" class="history-item"><div class="history-date">{{new Date(item.due_at).toLocaleString()}}</div><div class="history-prompt">{{item.pattern}}</div><div class="history-answer">{{item.reason}}</div><span class="pill">优先级 {{Math.round(item.priority*100)}}%</span></article><p v-if="!reviews.length" class="empty">目前没有待复习项目。</p></div></section>
      <section v-else-if="page==='settings'" class="content-page"><div class="eyebrow">LLM PROVIDER</div><h1>连接你自己的模型。</h1><p class="lede">API Key 只用于本地调用，列表和响应不会返回完整密钥。</p><div class="settings-card"><label>Provider Name<input v-model="provider.name"></label><label>Provider Type<select v-model="provider.type"><option value="openai">OpenAI</option><option value="openai-compatible">OpenAI-Compatible</option><option value="ollama">Ollama</option></select></label><label>Base URL<input v-model="provider.base_url" placeholder="https://api.openai.com/v1"></label><label>API Key<input v-model="provider.api_key" type="password" autocomplete="off"></label><label>Model<input v-model="provider.model"></label><div class="settings-grid"><label>Timeout<input v-model.number="provider.timeout" type="number"></label><label>Temperature<input v-model.number="provider.temperature" type="number" step=".1"></label><label>Max Tokens<input v-model.number="provider.max_tokens" type="number"></label></div><label class="checkbox"><input v-model="provider.enabled" type="checkbox"> Enabled</label><div class="action-row"><button class="primary" @click="testProvider">Test connection</button><button class="primary" @click="saveProvider">Save provider</button></div><p v-if="message" class="notice">{{message}}</p></div></section>
      <section v-else class="content-page"><div class="eyebrow">{{page.toUpperCase()}}</div><h1>这一部分正在准备中。</h1><p class="lede">核心练习、评估、反馈和历史记录已经可以使用。</p></section>
    </main>
  </div>
</template>
