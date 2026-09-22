const $ = selector => document.querySelector(selector)
const speech = window.SpeechPlayback
const api = (url, options) => fetch(url, { headers: { 'Content-Type': 'application/json' }, ...options }).then(async response => {
  const data = await response.json()
  if (!response.ok) throw new Error(data.error || 'Request failed')
  return data
})

let current = null
let providerId = ''
let nextLoading = false
let lastAttemptId = ''
let sessionId = ''
let activeSpeechButton = null

function escapeHTML(value) {
  return String(value || '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character])
}

function stopSpeech() {
  if (speech) speech.stop()
  if (activeSpeechButton) activeSpeechButton.textContent = '🔊'
  activeSpeechButton = null
}

function speakFromButton(button) {
  const text = decodeURIComponent(button.dataset.text || '')
  if (!speech || !speech.isSupported() || !text.trim()) return
  stopSpeech()
  activeSpeechButton = button
  button.textContent = '⏹'
  const started = speech.speak(text, {
    onend: () => { if (activeSpeechButton === button) { button.textContent = '🔊'; activeSpeechButton = null } },
    onerror: () => { if (activeSpeechButton === button) { button.textContent = '🔊'; activeSpeechButton = null } },
  })
  if (!started) { button.textContent = '🔊'; activeSpeechButton = null }
}

function attachSpeechButtons(root = document) {
  root.querySelectorAll('.speech-button[data-text]').forEach(button => { button.onclick = () => speakFromButton(button) })
}

function sentenceBlock(label, text, className = '') {
  const value = String(text || '').trim()
  if (!value) return ''
  return `<div class="speech-sentence ${className}"><div class="speech-label"><span>${escapeHTML(label)}</span><button type="button" class="speech-button" data-text="${encodeURIComponent(value)}" aria-label="Read ${escapeHTML(label)} aloud" title="Read ${escapeHTML(label)} aloud">🔊</button></div><p>${escapeHTML(value)}</p></div>`
}

function updateInputSpeech() {
  const button = $('#speak-input')
  const value = $('#answer').value.trim()
  const supported = !!speech && speech.isSupported()
  button.disabled = !supported || !value || value.length > speech.maxChars
  button.title = !supported ? 'Speech playback is not supported by this browser.' : value.length > speech.maxChars ? `Text is longer than ${speech.maxChars} characters.` : 'Read current answer aloud'
}

function setNextLoading(loading, message = '') {
  nextLoading = loading
  const status = $('#exercise-status')
  const button = $('#submit')
  const answer = $('#answer')
  if (loading) {
    status.className = 'exercise-status loading-state'
    status.innerHTML = `${message || 'Loading next question'}<span class="loading-dots">...</span>`
    status.removeAttribute('hidden')
    button.disabled = true
    button.textContent = 'Loading…'
    answer.disabled = true
    $('#prompt').classList.add('prompt-loading')
    $('#feedback').classList.add('hidden')
    return
  }
  status.className = `exercise-status${message ? ' exercise-status-error' : ''}`
  status.textContent = message
  if (!message) status.setAttribute('hidden', '')
  button.disabled = false
  button.textContent = 'Submit'
  answer.disabled = false
  $('#prompt').classList.remove('prompt-loading')
  updateInputSpeech()
}

async function ensureSession() {
  if (sessionId) return sessionId
  const session = await api('/api/sessions', { method: 'POST', body: JSON.stringify({ mode: 'adaptive' }) })
  sessionId = session.session_id
  return sessionId
}

function renderExercise() {
  if (!current) return
  stopSpeech()
  $('#prompt').textContent = current.chinese_prompt
  $('#pattern').textContent = current.target_pattern
  $('#difficulty').textContent = `Difficulty ${Number(current.difficulty).toFixed(1)} / 8`
  $('#answer').value = ''
  $('#feedback').classList.add('hidden')
  updateInputSpeech()
}

async function next() {
  if (nextLoading) return
  stopSpeech()
  setNextLoading(true)
  try {
    await ensureSession()
    current = await api('/api/practice/next', { method: 'POST', body: JSON.stringify({ mode: 'adaptive' }) })
    renderExercise()
    setNextLoading(false)
  } catch (error) {
    setNextLoading(false, error.message || 'Unable to load the next question.')
    throw error
  }
}

function renderEvaluation(result) {
  const feedback = $('#feedback')
  feedback.classList.remove('hidden')
  lastAttemptId = result.attempt_id || lastAttemptId
  if (!result.evaluation) {
    feedback.innerHTML = `<b>AI evaluation temporarily failed</b><p>${escapeHTML(result.error || 'The attempt was saved and mastery was not updated.')}</p><button class="retry">Re-evaluate</button>`
    feedback.querySelector('.retry').onclick = () => reevaluate(result.attempt_id)
    feedback.scrollIntoView({ behavior: 'smooth', block: 'start' })
    return
  }
  const evaluation = result.evaluation
  const natural = evaluation.more_natural_needed && evaluation.more_natural ? evaluation.more_natural : ''
  let body = `<b>${escapeHTML(({ correct: 'Correct', mostly_correct: 'Mostly correct', needs_improvement: 'Needs improvement', incorrect: 'Try again' })[evaluation.verdict] || evaluation.verdict)}</b><button class="next">Next →</button>`
  body += sentenceBlock('Your answer', $('#answer').value, 'your-answer')
  body += natural ? sentenceBlock('More natural', natural, 'more-natural') : ''
  body += !natural ? sentenceBlock('Suggested answer', evaluation.suggested_answer, 'suggested-answer') : (evaluation.suggested_answer && evaluation.suggested_answer !== natural ? sentenceBlock('Suggested answer', evaluation.suggested_answer, 'suggested-answer') : '')
  body += sentenceBlock('Alternative', evaluation.alternative, 'alternative')
  body += `<p>${escapeHTML(evaluation.explanation_zh)}</p><div class="score">Meaning <b>${Math.round(evaluation.meaning_score * 100)}%</b></div><div class="score">Grammar <b>${Math.round(evaluation.grammar_score * 100)}%</b></div><div class="score">Naturalness <b>${Math.round(evaluation.naturalness_score * 100)}%</b></div><div class="score">Pattern <b>${Math.round(evaluation.pattern_score * 100)}%</b></div>`
  feedback.innerHTML = body
  feedback.innerHTML += '<div class="user-feedback"><small>Quick signal</small><div><button data-feedback="too_easy">Too easy</button><button data-feedback="too_hard">Too hard</button><button data-feedback="unnatural">Unnatural</button><button data-feedback="evaluation_inaccurate">Evaluation inaccurate</button><button data-feedback="repetitive">Repetitive</button></div></div>'
  attachSpeechButtons(feedback)
  feedback.querySelectorAll('[data-feedback]').forEach(button => { button.onclick = () => sendFeedback(button.dataset.feedback) })
  feedback.querySelector('.next').onclick = next
  feedback.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

async function submit() {
  if (nextLoading || !current || !$('#answer').value.trim()) return
  stopSpeech()
  const button = $('#submit')
  const feedback = $('#feedback')
  button.disabled = true
  button.textContent = 'Evaluating…'
  feedback.classList.remove('hidden')
  feedback.innerHTML = '<p class="loading-state" aria-live="polite">Evaluating<span class="loading-dots">...</span></p>'
  feedback.scrollIntoView({ behavior: 'smooth', block: 'start' })
  try {
    renderEvaluation(await api('/api/attempts', { method: 'POST', body: JSON.stringify({ session_id: sessionId, exercise_id: current.exercise_id, answer: $('#answer').value }) }))
  } catch (error) {
    feedback.innerHTML = `<b>Evaluation failed</b><p>${escapeHTML(error.message || 'Please try again.')}</p>`
  } finally {
    button.disabled = false
    button.textContent = 'Submit'
    updateInputSpeech()
  }
}

async function sendFeedback(type) {
  if (!current || !lastAttemptId) return
  try {
    await api('/api/feedback', { method: 'POST', body: JSON.stringify({ exercise_id: current.exercise_id, attempt_id: lastAttemptId, session_id: sessionId, feedback_type: type }) })
    document.querySelectorAll('[data-feedback]').forEach(button => { button.disabled = true })
  } catch (error) { console.warn('feedback was not recorded', error) }
}

async function reevaluate(attemptId) {
  stopSpeech()
  const feedback = $('#feedback')
  feedback.innerHTML = '<p class="loading-state" aria-live="polite">Re-evaluating<span class="loading-dots">...</span></p>'
  try { renderEvaluation(await api(`/api/attempts/${attemptId}/reevaluate`, { method: 'POST' })) } catch (error) { feedback.innerHTML = `<b>Re-evaluation failed</b><p>${escapeHTML(error.message || 'Please try again.')}</p>` }
}

function populateVoices(voices) {
  const select = $('#speech-voice')
  if (!select) return
  const currentId = speech.selectedVoiceId()
  select.innerHTML = '<option value="">Auto</option>'
  voices.filter(voice => /^en(?:-|_)/i.test(voice.lang || '')).forEach(voice => {
    const option = document.createElement('option')
    option.value = voice.voiceURI || voice.name
    option.textContent = `${voice.name} (${voice.lang})`
    select.appendChild(option)
  })
  select.value = currentId
  select.disabled = !speech.isSupported() || voices.length === 0
}

function setupSpeechSettings() {
  const support = $('#speech-support')
  const voice = $('#speech-voice')
  const rate = $('#speech-rate')
  if (!speech || !speech.isSupported()) {
    support.textContent = 'Speech playback is not supported by this browser.'
    voice.disabled = true
    rate.disabled = true
    $('#speak-input').disabled = true
    return
  }
  support.textContent = 'Browser SpeechSynthesis · local playback only'
  rate.value = String(speech.rate())
  speech.onVoicesChanged(populateVoices)
  voice.onchange = () => speech.setVoice(voice.value)
  rate.onchange = () => speech.setRate(rate.value)
  populateVoices(speech.getVoices())
  updateInputSpeech()
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

async function loadProvider() { const providers = await api('/api/providers'); if (providers.length) fillProvider(providers[0]) }
async function saveProvider() { const enteredKey = $('#provider-key').value; const saved = await api('/api/providers', { method: 'POST', body: JSON.stringify(providerPayload()) }); fillProvider(saved, true); $('#provider-key').value = enteredKey; $('#provider-message').textContent = enteredKey ? 'Provider 已保存。' : 'Provider 已保存，未填写 API Key，已保留已有 Key。' }
async function testProvider() { const result = await api('/api/providers/test', { method: 'POST', body: JSON.stringify(providerPayload()) }); $('#provider-message').textContent = result.ok ? `Connection successful (${result.latency_ms} ms).` : `Connection failed: ${result.error || 'unknown error'}` }

async function show(page) {
  stopSpeech()
  document.querySelectorAll('main > section').forEach(section => section.classList.add('hidden'))
  $(`#${page}`).classList.remove('hidden')
  document.querySelectorAll('nav button').forEach(button => button.classList.toggle('active', button.dataset.page === page))
  if (page === 'progress') { const p = await api('/api/progress'); $('#progress').innerHTML = `<div class="panel"><div class="row"><span>Recent practice</span><b>${p.recent_attempts}</b></div><div class="row"><span>Success rate</span><b>${Math.round(p.recent_success_rate * 100)}%</b></div><div class="row"><span>Global Difficulty</span><b>${Number(p.global_difficulty).toFixed(1)}</b></div><div class="row"><span>Due reviews / probes</span><b>${p.due_reviews || 0} / ${p.probe_count || 0}</b></div><h2>Patterns to practice</h2>${p.weak_patterns.map(x => `<div class="row"><span>${escapeHTML(x.pattern)}<small> retention ${Math.round((x.retention || 0) * 100)}% · transfer ${Math.round((x.transfer || 0) * 100)}%</small></span><span class="pill">${Math.round(x.mastery * 100)}%</span></div>`).join('')}</div>` }
  if (page === 'history') { const history = await api('/api/history'); $('#history').innerHTML = `<div class="panel">${history.length ? history.map(x => `<div class="row"><span>${escapeHTML(x.prompt)}<small>${escapeHTML(x.pattern_id)} · ${escapeHTML(x.scene_id)} · ${escapeHTML(x.intent_id || 'intent')} · D${Number(x.difficulty || 0).toFixed(1)} · ${escapeHTML(x.selection_reason || 'current_level')}${x.is_review ? ' · review' : ''}${x.is_probe ? ' · probe' : ''}</small></span><span>${escapeHTML(x.answer)}</span><span class="pill">${escapeHTML(x.verdict)}</span></div>`).join('') : '<p>No practice history yet.</p>'}</div>` }
  if (page === 'review') { const reviews = await api('/api/reviews'); $('#review').innerHTML = `<div class="panel"><h2>Due reviews</h2>${reviews.map(x => `<div class="row"><span>${escapeHTML(x.pattern)}</span><span class="pill">${new Date(x.due_at).toLocaleString()}</span></div>`).join('')}</div>` }
  if (page === 'scenes') $('#scenes').innerHTML = '<div class="panel"><h2>Scenes</h2><p>Select a scene from the Vue interface or use the practice API.</p></div>'
  if (page === 'settings') await loadProvider()
}

document.querySelectorAll('nav button').forEach(button => { button.onclick = () => show(button.dataset.page) })
$('#answer').addEventListener('input', updateInputSpeech)
$('#speak-input').onclick = () => { if (speech && speech.isSupported()) { stopSpeech(); activeSpeechButton = $('#speak-input'); $('#speak-input').textContent = '⏹'; speech.speak($('#answer').value.trim(), { onend: () => { $('#speak-input').textContent = '🔊'; activeSpeechButton = null }, onerror: () => { $('#speak-input').textContent = '🔊'; activeSpeechButton = null } }) } }
$('#submit').onclick = submit
$('#provider-save').onclick = saveProvider
$('#provider-test').onclick = testProvider
window.addEventListener('beforeunload', stopSpeech)
setupSpeechSettings()
next().catch(error => { $('#prompt').textContent = error.message || 'Unable to load the next question.' })
