<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

const events = ref([])
const summary = ref({
  total_events: 0,
  denials: 0,
  stop_rejections: 0,
  last_24h: 0
})
const timeseries = ref([])
const topTools = ref([])
const selectedRange = ref('7d')
const loading = ref(false)
const error = ref(null)
const autoRefresh = ref(true)
let refreshInterval = null

// Filters
const selectedType = ref('ALL')
const searchQuery = ref('')

// Modal
const selectedEvent = ref(null)
const copySuccess = ref(false)

const fetchMetrics = async () => {
  loading.value = true
  error.value = null
  try {
    const [eventsRes, summaryRes, tsRes, toolsRes] = await Promise.all([
      fetch('/api/metrics?limit=300'),
      fetch('/api/metrics/summary'),
      fetch(`/api/metrics/timeseries?range=${selectedRange.value}`),
      fetch('/api/metrics/top-tools')
    ])
    if (eventsRes.ok) {
      events.value = await eventsRes.json()
    } else {
      throw new Error('Failed to fetch events: ' + eventsRes.statusText)
    }
    if (summaryRes.ok) {
      summary.value = await summaryRes.json()
    }
    if (tsRes.ok) {
      timeseries.value = await tsRes.json()
    }
    if (toolsRes.ok) {
      topTools.value = await toolsRes.json()
    }
  } catch (err) {
    error.value = err.message || 'Failed to connect to metrics server'
  } finally {
    loading.value = false
  }
}

const changeRange = (rng) => {
  selectedRange.value = rng
  fetchMetrics()
}

const toggleAutoRefresh = () => {
  autoRefresh.value = !autoRefresh.value
  if (autoRefresh.value) {
    startAutoRefresh()
  } else {
    stopAutoRefresh()
  }
}

const startAutoRefresh = () => {
  stopAutoRefresh()
  refreshInterval = setInterval(fetchMetrics, 10000)
}

const stopAutoRefresh = () => {
  if (refreshInterval) {
    clearInterval(refreshInterval)
    refreshInterval = null
  }
}

const filteredEvents = computed(() => {
  return events.value.filter(e => {
    if (selectedType.value !== 'ALL' && e.event_type !== selectedType.value) {
      return false
    }
    if (!searchQuery.value.trim()) {
      return true
    }
    const q = searchQuery.value.toLowerCase()
    const inType = (e.event_type || '').toLowerCase().includes(q)
    const inReason = (e.reason || '').toLowerCase().includes(q)
    const inPayload = JSON.stringify(e.payload || {}).toLowerCase().includes(q)
    return inType || inReason || inPayload
  })
})

const maxTimeseriesValue = computed(() => {
  if (!timeseries.value || timeseries.value.length === 0) return 10
  const max = Math.max(...timeseries.value.map(p => p.total))
  return max > 0 ? max : 10
})

const maxToolCount = computed(() => {
  if (!topTools.value || topTools.value.length === 0) return 1
  return Math.max(...topTools.value.map(t => t.count)) || 1
})

const openModal = (e) => {
  selectedEvent.value = e
  copySuccess.value = false
}

const closeModal = () => {
  selectedEvent.value = null
  copySuccess.value = false
}

const copyPayload = async () => {
  if (!selectedEvent.value) return
  const text = JSON.stringify(selectedEvent.value.payload, null, 2)
  try {
    await navigator.clipboard.writeText(text)
    copySuccess.value = true
    setTimeout(() => { copySuccess.value = false }, 2000)
  } catch (e) {
    console.error('Failed to copy', e)
  }
}

const formatDate = (isoString) => {
  if (!isoString) return '-'
  const d = new Date(isoString)
  return d.toLocaleString()
}

const formatShortDate = (isoString) => {
  if (!isoString) return ''
  const d = new Date(isoString)
  return `${d.getMonth() + 1}/${d.getDate()} ${d.getHours()}:00`
}

const badgeClass = (eventType) => {
  switch (eventType) {
    case 'PRIMARY_TOOL_DENIED':
      return 'bg-red-500/15 text-red-400 border-red-500/30'
    case 'STOP_REQUESTED':
      return 'bg-amber-500/15 text-amber-400 border-amber-500/30'
    default:
      return 'bg-sky-500/15 text-sky-400 border-sky-500/30'
  }
}

const exportJSON = () => {
  const dataStr = 'data:text/json;charset=utf-8,' + encodeURIComponent(JSON.stringify(filteredEvents.value, null, 2))
  const downloadAnchor = document.createElement('a')
  downloadAnchor.setAttribute('href', dataStr)
  downloadAnchor.setAttribute('download', `attention-metrics-${Date.now()}.json`)
  document.body.appendChild(downloadAnchor)
  downloadAnchor.click()
  downloadAnchor.remove()
}

const exportCSV = () => {
  const headers = ['ID', 'Event Type', 'Reason', 'Timestamp']
  const rows = filteredEvents.value.map(e => [
    e.id,
    `"${(e.event_type || '').replace(/"/g, '""')}"`,
    `"${(e.reason || '').replace(/"/g, '""')}"`,
    `"${new Date(e.created_at).toISOString()}"`
  ])
  const csvContent = 'data:text/csv;charset=utf-8,' + [headers.join(','), ...rows.map(r => r.join(','))].join('\n')
  const downloadAnchor = document.createElement('a')
  downloadAnchor.setAttribute('href', encodeURI(csvContent))
  downloadAnchor.setAttribute('download', `attention-metrics-${Date.now()}.csv`)
  document.body.appendChild(downloadAnchor)
  downloadAnchor.click()
  downloadAnchor.remove()
}

onMounted(() => {
  fetchMetrics()
  startAutoRefresh()
})

onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class="min-h-screen bg-slate-950 text-slate-100 antialiased p-6 sm:p-10 font-sans">
    <div class="max-w-7xl mx-auto space-y-8">
      
      <!-- Top Navigation & Header -->
      <header class="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4 border-b border-slate-800 pb-6">
        <div>
          <div class="flex items-center gap-3">
            <div class="w-9 h-9 rounded-lg bg-indigo-600 flex items-center justify-center font-bold text-white shadow-lg shadow-indigo-500/30">
              🛡️
            </div>
            <div class="flex items-center gap-2">
              <h1 class="text-2xl font-bold tracking-tight text-white">Attention Guard Telemetry</h1>
              <span class="text-xs font-semibold px-2 py-0.5 rounded bg-indigo-500/20 text-indigo-300 border border-indigo-500/40">v1.1.0</span>
            </div>
          </div>
          <p class="text-sm text-slate-400 mt-1">Real-time LLM agent guardrail events, denials, and Prometheus observability</p>
        </div>

        <div class="flex items-center gap-3">
          <a 
            href="/metrics" 
            target="_blank"
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700 transition-colors"
          >
            <span>📊</span>
            Prometheus /metrics
          </a>

          <button 
            @click="toggleAutoRefresh"
            :class="autoRefresh ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30' : 'bg-slate-800 text-slate-400 border-slate-700'"
            class="flex items-center gap-2 px-3 py-1.5 rounded-md text-xs font-medium border transition-colors cursor-pointer"
          >
            <span class="w-2 h-2 rounded-full" :class="autoRefresh ? 'bg-emerald-400 animate-pulse' : 'bg-slate-500'"></span>
            Auto-refresh (10s)
          </button>

          <button 
            @click="fetchMetrics" 
            :disabled="loading"
            class="flex items-center gap-2 bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white px-3.5 py-1.5 rounded-md text-xs font-medium transition-colors shadow-sm cursor-pointer"
          >
            <svg v-if="loading" class="animate-spin h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"></path>
            </svg>
            <span v-else>↻</span>
            Refresh
          </button>
        </div>
      </header>

      <!-- Error Alert -->
      <div v-if="error" class="bg-red-950/50 border border-red-500/40 text-red-200 px-4 py-3 rounded-lg text-sm flex items-center justify-between">
        <div class="flex items-center gap-2">
          <span>⚠️</span>
          <span>{{ error }}</span>
        </div>
        <button @click="error = null" class="text-xs text-red-300 hover:text-white underline">Dismiss</button>
      </div>

      <!-- KPI Summary Cards -->
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <!-- Total Events -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm">
          <div class="text-xs font-medium text-slate-400 uppercase tracking-wider">Total Events</div>
          <div class="mt-2 flex items-baseline justify-between">
            <div class="text-3xl font-extrabold text-white">{{ summary.total_events }}</div>
            <span class="text-xs px-2 py-0.5 rounded bg-slate-800 text-slate-300 border border-slate-700">All time</span>
          </div>
        </div>

        <!-- Tool Denials -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm">
          <div class="text-xs font-medium text-red-400 uppercase tracking-wider">Tool Denials</div>
          <div class="mt-2 flex items-baseline justify-between">
            <div class="text-3xl font-extrabold text-red-400">{{ summary.denials }}</div>
            <span class="text-xs px-2 py-0.5 rounded bg-red-950/60 text-red-300 border border-red-800/40">PRIMARY_TOOL_DENIED</span>
          </div>
        </div>

        <!-- Stop Rejections -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm">
          <div class="text-xs font-medium text-amber-400 uppercase tracking-wider">Stop Rejections</div>
          <div class="mt-2 flex items-baseline justify-between">
            <div class="text-3xl font-extrabold text-amber-400">{{ summary.stop_rejections }}</div>
            <span class="text-xs px-2 py-0.5 rounded bg-amber-950/60 text-amber-300 border border-amber-800/40">STOP_REQUESTED</span>
          </div>
        </div>

        <!-- Last 24 Hours -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm">
          <div class="text-xs font-medium text-emerald-400 uppercase tracking-wider">Recent Activity</div>
          <div class="mt-2 flex items-baseline justify-between">
            <div class="text-3xl font-extrabold text-emerald-400">{{ summary.last_24h }}</div>
            <span class="text-xs px-2 py-0.5 rounded bg-emerald-950/60 text-emerald-300 border border-emerald-800/40">Past 24 hours</span>
          </div>
        </div>
      </div>

      <!-- Analytics Charts Section -->
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        
        <!-- Activity Timeline Sparkline / Chart (2 cols) -->
        <div class="lg:col-span-2 bg-slate-900 border border-slate-800 rounded-xl p-6 shadow-sm flex flex-col justify-between">
          <div class="flex items-center justify-between mb-4">
            <div>
              <h2 class="text-base font-semibold text-white">Event Trend & Denials</h2>
              <p class="text-xs text-slate-400">Activity volume and primary tool denials over time</p>
            </div>
            <div class="flex items-center gap-1 bg-slate-950 p-1 rounded-lg border border-slate-800 text-xs">
              <button 
                @click="changeRange('24h')" 
                :class="selectedRange === '24h' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white'"
                class="px-2.5 py-1 rounded cursor-pointer transition-colors font-medium"
              >
                24h
              </button>
              <button 
                @click="changeRange('7d')" 
                :class="selectedRange === '7d' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white'"
                class="px-2.5 py-1 rounded cursor-pointer transition-colors font-medium"
              >
                7d
              </button>
              <button 
                @click="changeRange('30d')" 
                :class="selectedRange === '30d' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white'"
                class="px-2.5 py-1 rounded cursor-pointer transition-colors font-medium"
              >
                30d
              </button>
            </div>
          </div>

          <!-- SVG Chart -->
          <div class="h-44 w-full relative flex items-end gap-1.5 pt-4 border-b border-slate-800">
            <div v-if="timeseries.length === 0" class="absolute inset-0 flex items-center justify-center text-xs text-slate-500">
              No activity recorded in this time range
            </div>
            <div 
              v-for="(point, idx) in timeseries" 
              :key="idx" 
              class="flex-1 flex flex-col items-center justify-end h-full group relative"
            >
              <!-- Bar total -->
              <div 
                class="w-full rounded-t bg-indigo-500/40 hover:bg-indigo-500/70 transition-all relative flex flex-col justify-end overflow-hidden"
                :style="{ height: `${Math.max(6, (point.total / maxTimeseriesValue) * 100)}%` }"
              >
                <!-- Red portion for denials -->
                <div 
                  v-if="point.denials > 0"
                  class="w-full bg-red-500/80 rounded-t"
                  :style="{ height: `${(point.denials / point.total) * 100}%` }"
                ></div>
              </div>

              <!-- Tooltip -->
              <div class="absolute bottom-full mb-2 hidden group-hover:flex flex-col bg-slate-950 border border-slate-700 p-2 rounded text-[10px] text-white shadow-xl z-20 whitespace-nowrap">
                <span class="font-bold text-slate-300">{{ formatShortDate(point.timestamp) }}</span>
                <span class="text-indigo-300">Total: {{ point.total }}</span>
                <span class="text-red-400" v-if="point.denials > 0">Denials: {{ point.denials }}</span>
                <span class="text-amber-400" v-if="point.stop_rejections > 0">Stop Rejections: {{ point.stop_rejections }}</span>
              </div>
            </div>
          </div>

          <div class="flex items-center justify-between text-[11px] text-slate-400 mt-2">
            <div class="flex items-center gap-4">
              <div class="flex items-center gap-1.5">
                <span class="w-2.5 h-2.5 rounded-sm bg-indigo-500/60"></span>
                <span>Total Activity</span>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="w-2.5 h-2.5 rounded-sm bg-red-500/80"></span>
                <span>Denials</span>
              </div>
            </div>
            <span>{{ timeseries.length }} time buckets</span>
          </div>
        </div>

        <!-- Top Denied Tools Breakdown (1 col) -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-6 shadow-sm flex flex-col justify-between">
          <div>
            <h2 class="text-base font-semibold text-white">Top Denied Tools</h2>
            <p class="text-xs text-slate-400 mb-4">Tools most frequently blocked by Attention Guard</p>

            <div v-if="topTools.length === 0" class="text-xs text-slate-500 py-8 text-center">
              No tool denials recorded yet
            </div>

            <div v-else class="space-y-3">
              <div v-for="t in topTools" :key="t.tool" class="space-y-1">
                <div class="flex items-center justify-between text-xs">
                  <span class="font-mono text-slate-200 truncate max-w-[170px]">{{ t.tool }}</span>
                  <span class="text-red-400 font-semibold">{{ t.count }} blocked</span>
                </div>
                <div class="w-full bg-slate-800 rounded-full h-1.5 overflow-hidden">
                  <div 
                    class="bg-gradient-to-r from-red-500 to-amber-500 h-1.5 rounded-full" 
                    :style="{ width: `${(t.count / maxToolCount) * 100}%` }"
                  ></div>
                </div>
              </div>
            </div>
          </div>

          <div class="text-[11px] text-slate-500 border-t border-slate-800/80 pt-3 mt-4 flex items-center justify-between">
            <span>Enforced via enforce-delegation.py</span>
            <span class="text-emerald-400">● Active</span>
          </div>
        </div>

      </div>

      <!-- Events Explorer -->
      <div class="bg-slate-900 border border-slate-800 rounded-xl shadow-sm overflow-hidden">
        
        <!-- Filter and Search Header -->
        <div class="p-4 sm:p-5 border-b border-slate-800 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-xs font-medium text-slate-400 mr-1">Filter:</span>
            <button 
              v-for="type in ['ALL', 'PRIMARY_TOOL_DENIED', 'STOP_REQUESTED']" 
              :key="type"
              @click="selectedType = type"
              :class="selectedType === type ? 'bg-indigo-600 text-white' : 'bg-slate-800 text-slate-400 hover:text-white border-slate-700'"
              class="px-2.5 py-1 rounded-md text-xs font-medium border border-transparent transition-colors cursor-pointer"
            >
              {{ type }}
            </button>
          </div>

          <div class="flex items-center gap-3">
            <div class="relative w-full sm:w-64">
              <input 
                v-model="searchQuery"
                type="text" 
                placeholder="Search reasons or payloads..." 
                class="w-full bg-slate-950 border border-slate-800 rounded-md px-3 py-1.5 text-xs text-slate-200 placeholder-slate-500 focus:outline-none focus:border-indigo-500 transition-colors"
              />
              <span v-if="searchQuery" @click="searchQuery = ''" class="absolute right-2.5 top-1.5 text-slate-500 hover:text-white text-xs cursor-pointer">✕</span>
            </div>

            <!-- Export Buttons -->
            <button 
              @click="exportCSV" 
              title="Export filtered events as CSV"
              class="px-2.5 py-1.5 rounded-md text-xs font-medium bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700 transition-colors cursor-pointer flex items-center gap-1.5"
            >
              <span>📄</span>
              CSV
            </button>
            <button 
              @click="exportJSON" 
              title="Export filtered events as JSON"
              class="px-2.5 py-1.5 rounded-md text-xs font-medium bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700 transition-colors cursor-pointer flex items-center gap-1.5"
            >
              <span>{ }</span>
              JSON
            </button>
          </div>
        </div>

        <!-- Table -->
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead class="bg-slate-950/60 text-slate-400 uppercase tracking-wider text-[10px] border-b border-slate-800">
              <tr>
                <th class="px-5 py-3 font-semibold">ID</th>
                <th class="px-5 py-3 font-semibold">Event Type</th>
                <th class="px-5 py-3 font-semibold">Reason</th>
                <th class="px-5 py-3 font-semibold">Timestamp</th>
                <th class="px-5 py-3 font-semibold text-right">Payload</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-slate-800/60 font-sans">
              <tr v-if="filteredEvents.length === 0" class="text-center text-slate-500">
                <td colspan="5" class="py-8">No matching telemetry events found</td>
              </tr>
              <tr 
                v-for="event in filteredEvents" 
                :key="event.id"
                class="hover:bg-slate-800/30 transition-colors group"
              >
                <td class="px-5 py-3.5 font-mono text-slate-400">#{{ event.id }}</td>
                <td class="px-5 py-3.5 whitespace-nowrap">
                  <span 
                    :class="badgeClass(event.event_type)"
                    class="px-2 py-0.5 rounded border text-[11px] font-medium font-mono"
                  >
                    {{ event.event_type }}
                  </span>
                </td>
                <td class="px-5 py-3.5 text-slate-300 max-w-md truncate">
                  {{ event.reason || '-' }}
                </td>
                <td class="px-5 py-3.5 whitespace-nowrap text-slate-400">
                  {{ formatDate(event.created_at) }}
                </td>
                <td class="px-5 py-3.5 text-right whitespace-nowrap">
                  <button 
                    @click="openModal(event)"
                    class="px-2.5 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700 font-medium transition-colors cursor-pointer text-[11px]"
                  >
                    Inspect
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Table Footer -->
        <div class="px-5 py-3 bg-slate-950/40 border-t border-slate-800 text-[11px] text-slate-500 flex justify-between items-center">
          <div>Showing {{ filteredEvents.length }} of {{ events.length }} events</div>
          <div>Storage: PostgreSQL + VictoriaMetrics</div>
        </div>

      </div>

    </div>

    <!-- JSON Payload Modal -->
    <div 
      v-if="selectedEvent" 
      class="fixed inset-0 bg-slate-950/80 backdrop-blur-sm z-50 flex items-center justify-center p-4"
      @click.self="closeModal"
    >
      <div class="bg-slate-900 border border-slate-800 rounded-xl max-w-2xl w-full shadow-2xl overflow-hidden flex flex-col max-h-[85vh]">
        <div class="p-4 border-b border-slate-800 flex items-center justify-between">
          <div class="flex items-center gap-2">
            <span class="font-bold text-white text-sm">Event #{{ selectedEvent.id }} Payload</span>
            <span :class="badgeClass(selectedEvent.event_type)" class="px-2 py-0.5 rounded border text-[10px] font-mono">
              {{ selectedEvent.event_type }}
            </span>
          </div>
          <button @click="closeModal" class="text-slate-400 hover:text-white text-sm cursor-pointer">✕</button>
        </div>

        <div class="p-4 overflow-y-auto flex-1 font-mono text-xs text-slate-300 bg-slate-950">
          <pre>{{ JSON.stringify(selectedEvent.payload, null, 2) }}</pre>
        </div>

        <div class="p-3 border-t border-slate-800 bg-slate-900 flex justify-end gap-2">
          <button 
            @click="copyPayload"
            class="px-3 py-1.5 rounded-md bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium transition-colors cursor-pointer"
          >
            {{ copySuccess ? '✓ Copied!' : 'Copy JSON' }}
          </button>
          <button 
            @click="closeModal"
            class="px-3 py-1.5 rounded-md bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs font-medium border border-slate-700 transition-colors cursor-pointer"
          >
            Close
          </button>
        </div>
      </div>
    </div>

  </div>
</template>
