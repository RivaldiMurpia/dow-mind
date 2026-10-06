import { useState, useCallback, useEffect } from 'react'
import { Sidebar } from './components/Sidebar'
import { Topbar } from './components/Topbar'
import { Overview } from './pages/Overview'
import { Migration } from './pages/Migration'
import { Docs } from './pages/Docs'
import { Settings } from './pages/Settings'
import { Playground } from './pages/Playground'
import { Chunks } from './pages/Chunks'
import { Setup } from './pages/Setup'
import { Wizard } from './pages/onboarding/Wizard'
import { useTheme } from './lib/useTheme'
import { api, setAdminToken, isUnauthorizedError } from './lib/api'

export type Page = 'overview' | 'playground' | 'chunks' | 'migration' | 'docs' | 'settings' | 'setup'

export interface ProjectInfo {
  projectID: string
  chunkCount: number
}

type AppState = 'checking' | 'locked' | 'wizard' | 'dashboard'

function App() {
  const { theme, toggleTheme } = useTheme()
  const [appState, setAppState] = useState<AppState>('checking')
  const [page, setPage] = useState<Page>('overview')
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [activeProject, setActiveProject] = useState<string>('')
  const [tokenInput, setTokenInput] = useState('')
  const [tokenError, setTokenError] = useState('')
  // Shared query state: VAL-CROSS-010 requires that the query entered in the
  // Overview playground persists when navigating to the full Playground page.
  const [sharedQuery, setSharedQuery] = useState('')

  // First-run detection: check if config exists on load.
  // If no config, show wizard. If config exists, probe the API: when the
  // server has an admin token set, /api/* returns 401 and we show the
  // unlock screen instead of an empty dashboard. Note /api/setup/status
  // itself sits behind the token middleware, so a 401 there also means
  // "locked" — it must not fall through to the dashboard.
  const boot = useCallback(() => {
    api.setupStatus().then(
      (status) => {
        if (!status.configured) {
          setAppState('wizard')
          return
        }
        return api.listProjects().then(
          () => setAppState('dashboard'),
          (e) => setAppState(isUnauthorizedError(e) ? 'locked' : 'dashboard'),
        )
      },
      (e) => setAppState(isUnauthorizedError(e) ? 'locked' : 'dashboard'),
    )
  }, [])

  useEffect(() => {
    boot()
  }, [boot])

  const handleUnlock = useCallback(() => {
    const t = tokenInput.trim()
    if (!t) return
    setTokenError('')
    setAdminToken(t)
    api.listProjects().then(
      () => boot(),
      (e) => {
        setAdminToken('')
        setTokenError(isUnauthorizedError(e) ? 'Wrong token — try again.' : 'Could not reach the server.')
      },
    )
  }, [tokenInput, boot])

  const handleWizardComplete = useCallback(() => {
    setAppState('dashboard')
    setPage('overview')
  }, [])

  const handleSelectProject = useCallback((id: string) => {
    setActiveProject(id)
    setPage('overview')
  }, [])

  const handleNavigate = useCallback((p: Page) => {
    setPage(p)
  }, [])

  // VAL-CROSS-010: Navigate to Playground while preserving the query from
  // the Overview playground.
  const handleNavigateWithQuery = useCallback((p: Page, query: string) => {
    setSharedQuery(query)
    setPage(p)
  }, [])

  const handleProjectsLoaded = useCallback((projs: ProjectInfo[]) => {
    setProjects(projs)
    if (!activeProject && projs.length > 0) {
      setActiveProject(projs[0].projectID)
    }
  }, [activeProject])

  // Show wizard on first run (no config)
  if (appState === 'wizard') {
    return <Wizard onComplete={handleWizardComplete} theme={theme} onToggleTheme={toggleTheme} />
  }

  // Loading state
  if (appState === 'checking') {
    return (
      <div className="app-loading">
        <div className="brand-mark mono">e</div>
        <span>Loading…</span>
      </div>
    )
  }

  // Admin token gate: the server requires a Bearer token on /api/*.
  if (appState === 'locked') {
    return (
      <div className="app-loading">
        <div className="brand-mark mono">e</div>
        <span>DOW Mind is locked</span>
        <span style={{ opacity: 0.6, fontSize: 13 }}>Enter the admin token to unlock the dashboard.</span>
        <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
          <input
            className="input mono"
            type="password"
            value={tokenInput}
            onChange={(e) => setTokenInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') handleUnlock() }}
            placeholder="Admin token"
            autoFocus
          />
          <button className="btn primary" onClick={handleUnlock}>Unlock</button>
        </div>
        {tokenError && <span style={{ color: '#f66', fontSize: 13 }}>{tokenError}</span>}
      </div>
    )
  }

  return (
    <div className="app">
      <Sidebar
        page={page}
        onNavigate={handleNavigate}
        projects={projects}
        activeProject={activeProject}
        onSelectProject={handleSelectProject}
        onProjectsLoaded={handleProjectsLoaded}
      />
      <div className="main">
        <Topbar theme={theme} onToggleTheme={toggleTheme} activeProject={activeProject} page={page} />
        <div className="content">
          {page === 'overview' && <Overview activeProject={activeProject} onNavigate={handleNavigate} onNavigateWithQuery={handleNavigateWithQuery} sharedQuery={sharedQuery} onSharedQueryChange={setSharedQuery} onProjectsUpdated={setProjects} />}
          {page === 'playground' && <Playground activeProject={activeProject} sharedQuery={sharedQuery} onSharedQueryChange={setSharedQuery} />}
          {page === 'chunks' && <Chunks activeProject={activeProject} />}
          {page === 'migration' && <Migration activeProject={activeProject} projects={projects} />}
          {page === 'docs' && <Docs />}
          {page === 'settings' && <Settings />}
          {page === 'setup' && <Setup />}
        </div>
      </div>
    </div>
  )
}

export default App
