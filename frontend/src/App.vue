<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

const events = ref([])
const summary = ref({
  total_events: 0,
  denials: 0,
  stop_rejections: 0,
  last_24h: 0
})
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
    const [eventsRes, summaryRes] = await Promise.all([
      fetch('/api/metrics?limit=300'),
      fetch('/api/metrics/summary')
    ])
    if (eventsRes.ok) {
      events.value = await eventsRes.json()
    } else {
      throw new Error('Failed to fetch events: ' + eventsRes.statusText)
    }
    if (summaryRes.ok) {
      summary.value = await summaryRes.json()
    }
  } catch (err) {
    error.value = err.message || 'Failed to connect to metrics server'
  } finally {
    loading.value = false
  }
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

onMounted(() => {
  fetchMetrics()
  if (autoRefresh.value) {
    startAutoRefresh()
  }
})

onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class='min-h-screen bg-slate-950 text-slate-100 antialiased p-6 sm:p-10 font-sans'>
    <div class='max-w-7xl mx-auto space-y-8'>
      
      <!-- Top Navigation & Header -->
      <header class='flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4 border-b border-slate-800 pb-6'>
        <div>
          <div class='flex items-center gap-3'>
            <div class='w-9 h-9 rounded-lg bg-indigo-600 flex items-center justify-center font-bold text-white shadow-lg shadow-indigo-500/30'>
              🛡️
            </div>
            <h1 class='text-2xl font-bold tracking-tight text-white'>Attention Guard Telemetry</h1>
          </div>
          <p class='text-sm text-slate-400 mt-1'>Real-time LLM agent guardrail events, denials, and runtime metrics</p>
        </div>

        <div class='flex items-center gap-3'>
          <button 
            @click='toggleAutoRefresh'
            :class="autoRefresh ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30' : 'bg-slate-800 text-slate-400 border-slate-700'"
            class='flex items-center gap-2 px-3 py-1.5 rounded-md text-xs font-medium border transition-colors cursor-pointer'
          >
            <span class='w-2 h-2 rounded-full' :class="autoRefresh ? 'bg-emerald-400 animate-pulse' : 'bg-slate-500'"></span>
            Auto-refresh (10s)
          </button>

          <button 
            @click='fetchMetrics' 
            :disabled='loading'
            class='flex items-center gap-2 bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white px-3.5 py-1.5 rounded-md text-xs font-medium transition-colors shadow-sm cursor-pointer'
          >
            <svg v-if='loading' class='animate-spin h-3.5 w-3.5 text-white' fill='none' viewBox='0 0 24 24'>
              <circle class='opacity-25' cx='12' cy='12' r='10' stroke='currentColor' stroke-width='4'></circle>
              <path class='opacity-75' fill='currentColor' d='M4 12a8 8 0 018-8v8H4z'></path>
            </svg>
            <span v-else>↻</span>
            Refresh
          </button>
        </div>
      </header>

      <!-- Error Alert -->
      <div v-if='error' class='bg-red-950/50 border border-red-500/40 text-red-200 px-4 py-3 rounded-lg text-sm flex items-center justify-between'>
        <div class='flex items-center gap-2'>
          <span>⚠️</span>
          <span>{{ error }}</span>
        </div>
        <button @click='error = null' class='text-xs text-red-300 hover:text-white underline'>Dismiss</button>
      </div>

      <!-- KPI Summary Cards -->
      <div class='grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4'>
        <!-- Total Events -->
        <div class='bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm'>
          <div class='text-xs font-medium text-slate-400 uppercase tracking-wider'>Total Events</div>
          <div class='mt-2 flex items-baseline justify-between'>
            <div class='text-3xl font-extrabold text-white'>{{ summary.total_events }}</div>
            <span class='text-xs px-2 py-0.5 rounded bg-slate-800 text-slate-300 border border-slate-700'>All time</span>
          </div>
        </div>

        <!-- Tool Denials -->
        <div class='bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm'>
          <div class='text-xs font-medium text-red-400 uppercase tracking-wider'>Tool Denials</div>
          <div class='mt-2 flex items-baseline justify-between'>
            <div class='text-3xl font-extrabold text-red-400'>{{ summary.denials }}</div>
            <span class='text-xs px-2 py-0.5 rounded bg-red-950/60 text-red-300 border border-red-800/40'>PRIMARY_TOOL_DENIED</span>
          </div>
        </div>

        <!-- Stop Rejections -->
        <div class='bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm'>
          <div class='text-xs font-medium text-amber-400 uppercase tracking-wider'>Stop Rejections</div>
          <div class='mt-2 flex items-baseline justify-between'>
            <div class='text-3xl font-extrabold text-amber-400'>{{ summary.stop_rejections }}</div>
            <span class='text-xs px-2 py-0.5 rounded bg-amber-950/60 text-amber-300 border border-amber-800/40'>STOP_REQUESTED</span>
          </div>
        </div>

        <!-- Last 24 Hours -->
        <div class='bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm'>
          <div class='text-xs font-medium text-emerald-400 uppercase tracking-wider'>Recent Activity</div>
          <div class='mt-2 flex items-baseline justify-between'>
            <div class='text-3xl font-extrabold text-emerald-400'>{{ summary.last_24h }}</div>
            <span class='text-xs px-2 py-0.5 rounded bg-emerald-950/60 text-emerald-300 border border-emerald-800/40'>Past 24 hours</span>
          </div>
        </div>
      </div>

      <!-- Controls & Table Section -->
      <div class='bg-slate-900 border border-slate-800 rounded-xl shadow-sm overflow-hidden'>
        
        <!-- Filter Toolbar -->
        <div class='p-4 border-b border-slate-800 flex flex-col md:flex-row gap-3 items-stretch md:items-center justify-between'>
          <div class='flex flex-wrap items-center gap-2'>
            <label class='text-xs text-slate-400 font-medium'>Filter:</label>
            <button 
              v-for="type in ['ALL', 'PRIMARY_TOOL_DENIED', 'STOP_REQUESTED']"
              :key='type'
              @click='selectedType = type'
              :class="selectedType === type ? 'bg-indigo-600 text-white' : 'bg-slate-800 text-slate-300 hover:bg-slate-700'"
              class='px-2.5 py-1 rounded text-xs font-medium transition-colors cursor-pointer'
            >
              {{ type === 'ALL' ? 'All Types' : type }}
            </button>
          </div>

          <div class='relative min-w-[240px]'>
            <input 
              v-model='searchQuery' 
              type='text' 
              placeholder='Search reason or payload...' 
              class='w-full bg-slate-950 border border-slate-700 rounded-md px-3 py-1.5 text-xs text-slate-100 placeholder-slate-500 focus:outline-none focus:border-indigo-500'
            />
            <button 
              v-if='searchQuery' 
              @click="searchQuery = ''" 
              class='absolute right-2.5 top-1.5 text-slate-400 hover:text-white text-xs'
            >
              ✕
            </button>
          </div>
        </div>

        <!-- Table -->
        <div class='overflow-x-auto'>
          <table class='w-full text-left text-sm border-collapse'>
            <thead>
              <tr class='border-b border-slate-800 bg-slate-950/50 text-xs font-semibold text-slate-400 uppercase tracking-wider'>
                <th class='py-3.5 px-4'>Time</th>
                <th class='py-3.5 px-4'>Event Type</th>
                <th class='py-3.5 px-4'>Reason / Details</th>
                <th class='py-3.5 px-4 text-right'>Payload</th>
              </tr>
            </thead>
            <tbody class='divide-y divide-slate-800/60 font-mono text-xs'>
              <tr v-if='filteredEvents.length === 0'>
                <td colspan='4' class='py-12 text-center text-slate-500 font-sans text-sm'>
                  No telemetry events found matching your criteria.
                </td>
              </tr>
              <tr 
                v-for='event in filteredEvents' 
                :key='event.id'
                class='hover:bg-slate-800/40 transition-colors'
              >
                <td class='py-3 px-4 text-slate-400 whitespace-nowrap'>
                  {{ formatDate(event.created_at) }}
                </td>
                <td class='py-3 px-4 whitespace-nowrap'>
                  <span 
                    :class='badgeClass(event.event_type)'
                    class='px-2 py-0.5 rounded text-[11px] font-semibold border'
                  >
                    {{ event.event_type }}
                  </span>
                </td>
                <td class='py-3 px-4 text-slate-200 max-w-lg truncate font-sans text-xs' :title='event.reason'>
                  {{ event.reason || '(No reason specified)' }}
                </td>
                <td class='py-3 px-4 text-right whitespace-nowrap'>
                  <button 
                    @click='openModal(event)'
                    class='text-indigo-400 hover:text-indigo-300 bg-indigo-950/40 hover:bg-indigo-900/50 border border-indigo-700/40 px-2.5 py-1 rounded text-[11px] transition-colors cursor-pointer'
                  >
                    Inspect JSON
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Table Footer -->
        <div class='px-4 py-3 border-t border-slate-800 text-xs text-slate-400 flex items-center justify-between'>
          <span>Showing {{ filteredEvents.length }} of {{ events.length }} events</span>
          <span>Database: PostgreSQL</span>
        </div>
      </div>

    </div>

    <!-- Payload Inspector Modal -->
    <div 
      v-if='selectedEvent' 
      class='fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-xs p-4'
      @click.self='closeModal'
    >
      <div class='bg-slate-900 border border-slate-800 rounded-xl shadow-2xl max-w-2xl w-full max-h-[85vh] flex flex-col overflow-hidden'>
        <div class='px-5 py-4 border-b border-slate-800 flex items-center justify-between bg-slate-950/60'>
          <div>
            <h3 class='font-bold text-white text-base flex items-center gap-2'>
              <span>Event #{{ selectedEvent.id }}</span>
              <span :class='badgeClass(selectedEvent.event_type)' class='text-xs px-2 py-0.5 rounded border'>
                {{ selectedEvent.event_type }}
              </span>
            </h3>
            <p class='text-xs text-slate-400 mt-0.5'>{{ formatDate(selectedEvent.created_at) }}</p>
          </div>
          <button @click='closeModal' class='text-slate-400 hover:text-white text-lg font-bold'>✕</button>
        </div>

        <div class='p-5 overflow-y-auto flex-1 font-mono text-xs'>
          <div v-if='selectedEvent.reason' class='mb-4 p-3 bg-slate-950 rounded border border-slate-800 text-slate-300 font-sans text-xs'>
            <span class='font-semibold text-slate-400 block mb-1'>Reason:</span>
            {{ selectedEvent.reason }}
          </div>

          <div class='relative'>
            <pre class='bg-slate-950 p-4 rounded border border-slate-800 text-emerald-400 overflow-x-auto whitespace-pre-wrap'>{{ JSON.stringify(selectedEvent.payload, null, 2) }}</pre>
          </div>
        </div>

        <div class='px-5 py-3 border-t border-slate-800 bg-slate-950/60 flex items-center justify-between'>
          <button 
            @click='copyPayload' 
            class='text-xs bg-slate-800 hover:bg-slate-700 text-slate-200 px-3 py-1.5 rounded transition-colors flex items-center gap-1.5 cursor-pointer'
          >
            <span>📋</span>
            <span>{{ copySuccess ? 'Copied!' : 'Copy JSON' }}</span>
          </button>
          <button 
            @click='closeModal' 
            class='text-xs bg-indigo-600 hover:bg-indigo-500 text-white px-3.5 py-1.5 rounded font-medium transition-colors cursor-pointer'
          >
            Close
          </button>
        </div>
      </div>
    </div>

  </div>
</template>
