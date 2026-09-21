const $ = (selector) => document.querySelector(selector)
const api = (url, options) => fetch(url, { headers: { 'Content-Type': 'application/json' }, ...options }).then(async response => { const data = await response.json(); if (!response.ok) throw new Error(data.error || 'Request failed'); return data })
let current = null
let providerId = ''

function renderExercise() {
  if (!current) return
  $('#prompt').textContent = current.chinese_prompt
  $('#pattern').textContent = current.target_pattern
  $('#difficulty').textContent = `Difficulty ${Number(current.difficulty).toFixed(1)} / 8`
  $('#answer').value = ''
  $('#feedback').classList.add('hidden')
}

async function next() {
  current = await api('/api/practice/next', { method: 'POST', body: JSON.stringify({ mode: 'adaptive' }) })
  renderExercise()
}

function renderEvaluation(result) {
  const feedback = $('#feedback')
  feedback.classList.remove('hidden')
  if (!result.evaluation) {
    feedback.innerHTML = `<b>AI evaluation temporarily failed</b><p>${result.error || 'The attempt was saved and mastery was not updated.'}</p><button class="retry">Re-evaluate</button>`
    feedback.querySelector('.retry').onclick = () => reevaluate(result.attempt_id)
    return
  }
  const evaluation = result.evaluation
  feedback.innerHTML = `<b>${({ correct: 'Correct', mostly_correct: 'Mostly correct', needs_improvement: 'Needs improvement', incorrect: 'Try again' })[evaluation.verdict] || evaluation.verdict}</b><button class="next">Next →</button><p>${evaluation.explanation_zh}</p><p><strong>More natural:</strong> ${evaluation.suggested_answer}</p><div class="score">Meaning <b>${Math.round(evaluation.meaning_score * 100)}%</b></div><div class="score">Grammar <b>${Math.round(evaluation.grammar_score * 100)}%</b></div><div class="score">Naturalness <b>${Math.round(evaluation.naturalness_score * 100)}%</b></div><div class="score">Pattern <b>${Math.round(evaluation.pattern_score * 100)}%</b></div>`
  feedback.querySelector('.next').onclick = next
}

async function submit() {
  if (!current || !$('#answer').value.trim()) return
  renderEvaluation(await api('/api/attempts', { method: 'POST', body: JSON.stringify({ exercise_id: current.exercise_id, answer: $('#answer').value }) }))
}

async function reevaluate(attemptId) {
  renderEvaluation(await api(`/api/attempts/${attemptId}/reevaluate`, { method: 'POST' }))
}

function providerPayload() {
  return { id: providerId, name: $('#provider-name').value, type: $('#provider-type').value, base_url: $('#provider-url').value, api_key: $('#provider-key').value, model: $('#provider-model').value, timeout: Number($('#provider-timeout').value || 45), temperature: Number($('#provider-temperature').value || .2), max_tokens: Number($('#provider-tokens').value || 800), enabled: $('#provider-enabled').checked }
}

function fillProvider(provider, preserveKey = false) {
  providerId = provider.id || ''
  $('#provider-name').value = provider.name || ''
  $('#provider-type').value = provider.type || 'openai-compatible'
  $('#provider-url').value = provider.base_url || ''
  if (!preserveKey) $('#provider-key').value = ''
  $('#provider-model').value = provider.model || ''
  $('#provider-timeout').value = provider.timeout || 45
  $('#provider-temperature').value = provider.temperature ?? .2
  $('#provider-tokens').value = provider.max_tokens || 800
  $('#provider-enabled').checked = !!provider.enabled
}

async function loadProvider() {
  const providers = await api('/api/providers')
  if (providers.length) fillProvider(providers[0])
}

async function saveProvider() {
  const enteredKey = $('#provider-key').value
  const saved = await api('/api/providers', { method: 'POST', body: JSON.stringify(providerPayload()) })
  fillProvider(saved, true)
  $('#provider-key').value = enteredKey
  $('#provider-message').textContent = enteredKey ? 'Provider 已保存。' : 'Provider 已保存，未填写 API Key，已保留已有 Key。'
}

async function testProvider() {
  const result = await api('/api/providers/test', { method: 'POST', body: JSON.stringify(providerPayload()) })
  $('#provider-message').textContent = result.ok ? `Connection successful (${result.latency_ms} ms).` : `Connection failed: ${result.error || 'unknown error'}`
}

async function show(page) {
  document.querySelectorAll('main > section').forEach(section => section.classList.add('hidden'))
  $(`#${page}`).classList.remove('hidden')
  document.querySelectorAll('nav button').forEach(button => button.classList.toggle('active', button.dataset.page === page))
  if (page === 'progress') { const p = await api('/api/progress'); $('#progress').innerHTML = `<div class="panel"><div class="row"><span>Recent practice</span><b>${p.recent_attempts}</b></div><div class="row"><span>Success rate</span><b>${Math.round(p.recent_success_rate * 100)}%</b></div><div class="row"><span>Global Difficulty</span><b>${Number(p.global_difficulty).toFixed(1)}</b></div><h2>Patterns to practice</h2>${p.weak_patterns.map(x => `<div class="row"><span>${x.pattern}</span><span class="pill">${Math.round(x.mastery * 100)}%</span></div>`).join('')}</div>` }
  if (page === 'history') { const history = await api('/api/history'); $('#history').innerHTML = `<div class="panel">${history.length ? history.map(x => `<div class="row"><span>${x.prompt}</span><span>${x.answer}</span><span class="pill">${x.verdict}</span></div>`).join('') : '<p>No practice history yet.</p>'}</div>` }
  if (page === 'review') { const reviews = await api('/api/reviews'); $('#review').innerHTML = `<div class="panel"><h2>Due reviews</h2>${reviews.map(x => `<div class="row"><span>${x.pattern}</span><span class="pill">${new Date(x.due_at).toLocaleString()}</span></div>`).join('')}</div>` }
  if (page === 'scenes') $('#scenes').innerHTML = '<div class="panel"><h2>Scenes</h2><p>Select a scene from the Vue interface or use the practice API.</p></div>'
  if (page === 'settings') await loadProvider()
}

document.querySelectorAll('nav button').forEach(button => { button.onclick = () => show(button.dataset.page) })
$('#submit').onclick = submit
$('#provider-save').onclick = saveProvider
$('#provider-test').onclick = testProvider
next().catch(error => { $('#prompt').textContent = error.message })
