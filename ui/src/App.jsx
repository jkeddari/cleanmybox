import { useEffect, useMemo, useState } from 'react'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from './components/ui/card'

const STEP_PROGRESS = {
  queued: 8,
  initializing: 16,
  scanning_emails: 35,
  processing_newsletters: 62,
  processing_ai: 85,
  cleanplus_pending: 85,
  completed: 100,
  failed: 100,
}

const FEATURES = [
  {
    name: 'Clean',
    price: '7 EUR',
    description: 'Newsletter cleanup + automatic unsubscribe.',
    items: [
      'Header-based newsletter detection',
      'Mark as read + move to trash',
      'Automatic unsubscribe',
      'Detailed final report',
    ],
  },
  {
    name: 'CleanPlus',
    price: '11 EUR',
    description: 'Everything in Clean + cautious AI analysis of the rest.',
    items: [
      'Very cautious LLM analysis',
      'Delete spam + clearly useless',
      'Verdicts: spam, useless, legit, unsure',
      'Detailed final report',
    ],
  },
]

function App() {
  const [isLoggedIn, setIsLoggedIn] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [checkoutPlan, setCheckoutPlan] = useState('')
  const [job, setJob] = useState(null)
  const [jobError, setJobError] = useState('')
  const [nowMs, setNowMs] = useState(Date.now())

  const query = useMemo(() => new URLSearchParams(window.location.search), [])
  const isPaymentCanceled = query.get('canceled') === '1'
  const isPaymentSuccess = query.get('success') === '1'
  const rawSessionID = query.get('session_id') || ''
  const checkoutSessionID = rawSessionID === '{CHECKOUT_SESSION_ID}' ? '' : rawSessionID

  useEffect(() => {
    let isMounted = true
    fetch('/api/session', { credentials: 'include' })
      .then((res) => res.ok ? res.json() : { loggedIn: false })
      .then((data) => {
        if (!isMounted) return
        setIsLoggedIn(Boolean(data?.loggedIn))
      })
      .catch(() => {
        if (!isMounted) return
        setIsLoggedIn(false)
      })
      .finally(() => {
        if (!isMounted) return
        setIsLoading(false)
      })
    return () => {
      isMounted = false
    }
  }, [])

  const heroCta = useMemo(() => {
    if (isLoading) return { label: 'Loading...', href: '#' }
    if (isLoggedIn) return { label: 'Pick a plan', href: '#plans' }
    return { label: 'Login', href: '/auth/google' }
  }, [isLoading, isLoggedIn])

  useEffect(() => {
    if (!isPaymentSuccess || !checkoutSessionID) {
      return
    }

    let isMounted = true
    let timeoutID

    const poll = async () => {
      try {
        const response = await fetch(`/api/job/${encodeURIComponent(checkoutSessionID)}`, {
          credentials: 'include',
          cache: 'no-store',
        })

        if (!isMounted) return

        if (response.status === 404) {
          timeoutID = window.setTimeout(poll, 1500)
          return
        }

        if (!response.ok) {
          throw new Error('unable to fetch cleanup status')
        }

        const data = await response.json()
        if (!isMounted) return

        setJob(data)
        setJobError('')

        if (data?.status !== 'done' && data?.status !== 'error') {
          timeoutID = window.setTimeout(poll, 1500)
        }
      } catch (error) {
        if (!isMounted) return
        setJobError('Could not load cleanup progress yet. Retrying...')
        timeoutID = window.setTimeout(poll, 2500)
      }
    }

    poll()

    return () => {
      isMounted = false
      if (timeoutID) {
        window.clearTimeout(timeoutID)
      }
    }
  }, [checkoutSessionID, isPaymentSuccess])

  useEffect(() => {
    const intervalID = window.setInterval(() => setNowMs(Date.now()), 2000)
    return () => window.clearInterval(intervalID)
  }, [])

  const startCheckout = async (plan) => {
    if (!isLoggedIn || checkoutPlan) return
    setCheckoutPlan(plan)
    try {
      const response = await fetch('/api/checkout', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify({ plan }),
      })
      if (!response.ok) {
        throw new Error('checkout failed')
      }
      const data = await response.json()
      if (data?.url) {
        window.location.href = data.url
      }
    } catch (error) {
      setCheckoutPlan('')
      window.alert('Payment error. Please try again.')
    }
  }

  const progressValue = useMemo(() => {
    if (typeof job?.progress_percent === 'number') {
      return job.progress_percent
    }
    if (!job?.step) return isPaymentSuccess ? 10 : 0
    return STEP_PROGRESS[job.step] || 20
  }, [job?.progress_percent, job?.step, isPaymentSuccess])

  const isJobDone = job?.status === 'done'
  const isJobError = job?.status === 'error'
  const heartbeatTs = job?.last_heartbeat_at ? Date.parse(job.last_heartbeat_at) : NaN
  const heartbeatAgeSeconds = Number.isFinite(heartbeatTs) ? Math.max(0, Math.floor((nowMs - heartbeatTs) / 1000)) : null
  const staleAfterSeconds = typeof job?.stale_after_seconds === 'number' ? job.stale_after_seconds : 360
  const isHeartbeatStale = heartbeatAgeSeconds !== null && heartbeatAgeSeconds > staleAfterSeconds

  return (
    <div className="min-h-screen bg-[radial-gradient(circle_at_top_left,rgba(255,240,220,0.9),rgba(248,244,238,0.95)_55%,rgba(244,240,233,0.9)_100%)]">
      <div className="container space-y-16 py-10">
        <header className="flex flex-col gap-6 sm:flex-row sm:items-center sm:justify-between">
          <a href="/" className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-soft">
              C
            </div>
            <div>
              <p className="text-xl font-semibold tracking-tight">cleanmybox</p>
              <p className="text-xs text-muted-foreground">One-shot inbox cleanup</p>
            </div>
          </a>
          <div className="flex items-center gap-3">
            {isLoggedIn ? (
              <Badge>Connected</Badge>
            ) : (
              <Button variant="outline" asChild>
                <a href="/auth/google">Login</a>
              </Button>
            )}
          </div>
        </header>

        <main className="space-y-16">
          {isPaymentCanceled ? (
            <Card className="border-yellow-300 bg-yellow-50">
              <CardHeader>
                <CardTitle className="text-lg">Payment canceled</CardTitle>
                <CardDescription>Your checkout was canceled. You can choose a plan again anytime.</CardDescription>
              </CardHeader>
            </Card>
          ) : null}

          {isPaymentSuccess ? (
            <Card className="border-primary/30">
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <CardTitle className="text-xl">{isJobDone ? 'Cleanup result' : 'Cleanup in progress'}</CardTitle>
                    <CardDescription>
                      {isJobDone
                        ? 'Your cleanup is complete.'
                        : isJobError
                          ? 'Cleanup failed before completion.'
                          : 'Your payment is confirmed. Cleanup starts automatically.'}
                    </CardDescription>
                  </div>
                  <Badge variant="outline">
                    {job?.plan ? job.plan.toUpperCase() : 'PROCESSING'}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-5">
                <div className="h-2 overflow-hidden rounded-full bg-secondary">
                  <div
                    className="h-full rounded-full bg-primary transition-all duration-500"
                    style={{ width: `${progressValue}%` }}
                  />
                </div>

                <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                  <span>
                    {job?.current_email ? `Current: ${job.current_email}` : 'Current: waiting for next email'}
                  </span>
                  <span className="font-semibold text-foreground">
                    {job?.processed_count ?? 0} / {job?.total_count ?? 0} emails processed
                  </span>
                </div>

                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Deleted</p>
                    <p className="text-xl font-semibold">{job?.stats?.deleted ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Newsletters</p>
                    <p className="text-xl font-semibold">{job?.stats?.newsletters ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Spam</p>
                    <p className="text-xl font-semibold">{job?.stats?.spam ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Useless</p>
                    <p className="text-xl font-semibold">{job?.stats?.useless ?? 0}</p>
                  </div>
                </div>

                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Legit kept</p>
                    <p className="text-xl font-semibold">{job?.stats?.legit ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Unsure kept</p>
                    <p className="text-xl font-semibold">{job?.stats?.unsure ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Unsubscribed ok</p>
                    <p className="text-xl font-semibold">{job?.stats?.unsubscribed_ok ?? 0}</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-xs text-muted-foreground">Unsubscribed failed</p>
                    <p className="text-xl font-semibold">{job?.stats?.unsubscribed_failed ?? 0}</p>
                  </div>
                </div>

                <div className="grid gap-3 text-sm text-muted-foreground sm:grid-cols-2">
                  <p>Step: <span className="font-semibold text-foreground">{job?.step || 'waiting_webhook'}</span></p>
                  <p>Status: <span className="font-semibold text-foreground">{job?.status || 'pending'}</span></p>
                  <p>AI deleted: <span className="font-semibold text-foreground">{job?.stats?.ai_deleted ?? 0}</span></p>
                  <p>Total scanned: <span className="font-semibold text-foreground">{job?.stats?.total_scanned ?? 0}</span></p>
                  <p>
                    Heartbeat:{' '}
                    <span className={`font-semibold ${isHeartbeatStale ? 'text-red-600' : 'text-foreground'}`}>
                      {heartbeatAgeSeconds === null
                        ? 'waiting...'
                        : isHeartbeatStale
                          ? `stalled (${heartbeatAgeSeconds}s ago)`
                          : `alive (${heartbeatAgeSeconds}s ago)`}
                    </span>
                  </p>
                  <p>Dry run: <span className="font-semibold text-foreground">{job?.dry_run ? 'enabled' : 'disabled'}</span></p>
                </div>

                {job?.error ? (
                  <p className="text-sm font-medium text-red-600">{job.error}</p>
                ) : null}
                {jobError ? (
                  <p className="text-sm font-medium text-amber-700">{jobError}</p>
                ) : null}

                {(isJobDone || isJobError) ? (
                  <div className="flex flex-wrap gap-3 pt-1">
                    <Button asChild>
                      <a href="/">Run another cleanup</a>
                    </Button>
                    <Button variant="outline" asChild>
                      <a href="/#plans">See plans</a>
                    </Button>
                  </div>
                ) : null}
              </CardContent>
            </Card>
          ) : null}

          {!isPaymentSuccess ? (
            <>
              <section className="grid gap-10 lg:grid-cols-[1.1fr_0.9fr] lg:items-center">
            <div className="space-y-6">
              <Badge className="w-fit">Fast mailbox cleaning</Badge>
              <h1 className="text-4xl font-semibold leading-tight md:text-5xl">
                Delete the noise, keep the important.
              </h1>
              <p className="max-w-xl text-lg text-muted-foreground">
                CleanMyBox detects newsletters, unsubscribes, and cleans your inbox with a conservative AI option.
              </p>
              <div className="flex flex-wrap items-center gap-4">
                <Button size="lg" asChild>
                  <a href={heroCta.href}>{heroCta.label}</a>
                </Button>
                <span className="text-sm text-muted-foreground">One-shot, no accounts stored.</span>
              </div>
              <div className="grid gap-4 sm:grid-cols-3">
                <div className="rounded-2xl border bg-card p-4">
                  <p className="text-xl font-semibold">No retention</p>
                  <p className="text-xs text-muted-foreground">Login and data</p>
                </div>
                <div className="rounded-2xl border bg-card p-4">
                  <p className="text-xl font-semibold">2-5 min</p>
                  <p className="text-xs text-muted-foreground">Typical cleanup</p>
                </div>
                <div className="rounded-2xl border bg-card p-4">
                  <p className="text-xl font-semibold">AI powered</p>
                  <p className="text-xs text-muted-foreground">Optional for CleanPlus</p>
                </div>
              </div>
            </div>

            <Card>
              <CardHeader className="space-y-3">
                <div className="flex items-center justify-between">
                  <CardTitle>Results snapshot</CardTitle>
                  <Badge variant="outline">Example</Badge>
                </div>
                <CardDescription>See what a cleanup report looks like.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
                <div>
                  <p className="text-4xl font-semibold">1,284</p>
                  <p className="text-sm text-muted-foreground">emails deleted</p>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-lg font-semibold">923</p>
                    <p className="text-xs text-muted-foreground">newsletters</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-lg font-semibold">241</p>
                    <p className="text-xs text-muted-foreground">spam</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-lg font-semibold">84</p>
                    <p className="text-xs text-muted-foreground">useless</p>
                  </div>
                  <div className="rounded-xl bg-muted p-3">
                    <p className="text-lg font-semibold">36</p>
                    <p className="text-xs text-muted-foreground">unsure</p>
                  </div>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-secondary">
                  <div className="h-full w-3/4 animate-pulse rounded-full bg-primary" />
                </div>
              </CardContent>
            </Card>
              </section>

              <section className="grid gap-6 lg:grid-cols-[1fr_auto_1fr_auto_1fr] lg:items-center">
            {[
              {
                title: 'Login',
                text: 'Authenticate quickly with minimal access.',
              },
              {
                title: 'Choose Clean or CleanPlus',
                text: 'Clean removes newsletters. CleanPlus adds conservative AI review.',
              },
              {
                title: 'Get your report',
                text: 'See totals by category and unsubscribe success rate.',
              },
            ].map((item, index) => (
              <div key={item.title} className="contents">
                <Card>
                  <CardHeader>
                    <CardTitle className="text-lg">{item.title}</CardTitle>
                    <CardDescription>{item.text}</CardDescription>
                  </CardHeader>
                </Card>
                {index < 2 ? (
                  <div className="hidden h-10 w-10 items-center justify-center rounded-full border bg-card text-muted-foreground lg:flex">
                    →
                  </div>
                ) : null}
              </div>
            ))}
              </section>

              <section id="plans" className="space-y-6">
            <div className="flex flex-wrap items-end justify-between gap-4">
              <div>
                <h2 className="text-3xl font-semibold">Pick your cleanup</h2>
                <p className="text-muted-foreground">One-time payment, no subscription.</p>
              </div>
              <Badge variant="outline">Secure payment by Stripe</Badge>
            </div>
            <div className="grid gap-6 lg:grid-cols-2">
              {FEATURES.map((plan) => (
                <Card key={plan.name} className="flex h-full flex-col">
                  <CardHeader className="space-y-2">
                    <div className="flex items-center justify-between">
                      <CardTitle>{plan.name}</CardTitle>
                      <span className="text-2xl font-semibold text-primary">{plan.price}</span>
                    </div>
                    <CardDescription>{plan.description}</CardDescription>
                  </CardHeader>
                  <CardContent className="flex-1">
                    <ul className="space-y-2 text-sm text-muted-foreground">
                      {plan.items.map((item) => (
                        <li key={item} className="flex items-center gap-2">
                          <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                          {item}
                        </li>
                      ))}
                    </ul>
                  </CardContent>
                  <CardFooter>
                    {isLoggedIn ? (
                      <Button
                        className="w-full"
                        size="lg"
                        disabled={checkoutPlan !== ''}
                        onClick={() => startCheckout(plan.name.toLowerCase())}
                      >
                        {checkoutPlan === plan.name.toLowerCase() ? 'Redirecting...' : plan.name === 'Clean' ? 'Start Clean' : 'Start CleanPlus'}
                      </Button>
                    ) : (
                      <Button className="w-full" size="lg" variant="secondary" asChild>
                        <a href="/auth/google">Login</a>
                      </Button>
                    )}
                  </CardFooter>
                </Card>
              ))}
            </div>
              </section>

              <section className="space-y-6">
            <div>
              <h2 className="text-3xl font-semibold">FAQ</h2>
              <p className="text-muted-foreground">Quick answers before you clean.</p>
            </div>
            <div className="grid gap-6 lg:grid-cols-3">
              {[
                {
                  question: 'Do you store my emails?',
                  answer: 'No. We only process messages during the cleanup and keep short-lived job data.',
                },
                {
                  question: 'What does CleanPlus do?',
                  answer: 'It adds a conservative AI review to remove spam or clearly useless emails.',
                },
                {
                  question: 'Can I run it again later?',
                  answer: 'Yes. Each cleanup is a one-shot payment you can run whenever you want.',
                },
              ].map((item) => (
                <Card key={item.question}>
                  <CardHeader>
                    <CardTitle className="text-lg">{item.question}</CardTitle>
                    <CardDescription>{item.answer}</CardDescription>
                  </CardHeader>
                </Card>
              ))}
            </div>
              </section>
            </>
          ) : null}
        </main>

        <footer className="flex flex-wrap items-center justify-between gap-4 border-t pt-6 text-xs text-muted-foreground">
          <span>CleanMyBox - One-shot inbox cleanup</span>
          <span>Copyright © {new Date().getFullYear()} CleanMyBox</span>
        </footer>
      </div>
    </div>
  )
}

export default App
