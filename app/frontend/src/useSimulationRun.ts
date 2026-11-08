import { useCallback, useEffect, useRef, useState } from 'react';
import { ServiceError } from './api';
import { replaceRoute } from './routing';
import { canAutoAdvance, getRun, listRuns, runAction } from './simulation';
import type { Run, RunHeader } from './simulation';

export const loadingResult = (run: Run | null) => run?.status === 'queued' || run?.status === 'running';
const errorText = (error: unknown) => error instanceof ServiceError ? error.message : 'Не удалось обновить данные прогона.';
const playbackDelay = 1500;

export function openRun(id: string) {
  const url = new URL(window.location.href);
  url.hash = id ? `#simulation/${encodeURIComponent(id)}` : '#simulation';
  replaceRoute(url);
}

export function useSimulationRun(runId: string | undefined, onInteractionLock: (locked: boolean) => void) {
  const [run, setRun] = useState<Run | null>(null);
  const current = run?.id === runId ? run : null;
  const [runs, setRuns] = useState<RunHeader[]>([]);
  const [registryState, setRegistryState] = useState<'loading' | 'ready' | 'failed'>('loading');
  const [registryError, setRegistryError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [playing, setPlaying] = useState(false);
  const [readVersion, setReadVersion] = useState(0);
  const actionInProgress = useRef(false);
  const actionGeneration = useRef(0);
  const mounted = useRef(true);
  const advanceTimer = useRef<number | undefined>(undefined);
  const pause = useCallback(() => {
    window.clearTimeout(advanceTimer.current);
    setPlaying(false);
  }, []);

  useEffect(() => {
    pause(); setError(null);
  }, [runId, pause]);
  useEffect(() => {
    const onVisibilityChange = () => { if (document.hidden) pause(); };
    document.addEventListener('visibilitychange', onVisibilityChange);
    return () => document.removeEventListener('visibilitychange', onVisibilityChange);
  }, [pause]);

  useEffect(() => {
    const controller = new AbortController();
    setRegistryState('loading'); setRegistryError(null);
    void listRuns(controller.signal).then(value => {
      if (!controller.signal.aborted) { setRuns(value); setRegistryState('ready'); }
    }).catch(issue => {
      if (!controller.signal.aborted) {
        setRegistryState('failed');
        setRegistryError(errorText(issue));
      }
    });
    return () => controller.abort();
  }, [readVersion]);

  useEffect(() => {
    if (!runId) return;
    const controller = new AbortController();
    let timer: number | undefined;
    const read = async () => {
      if (actionInProgress.current) {
        timer = window.setTimeout(() => void read(), 650);
        return;
      }
      const generation = actionGeneration.current;
      try {
        const value = await getRun(runId, controller.signal);
        if (controller.signal.aborted) return;
        if (generation !== actionGeneration.current) {
          timer = window.setTimeout(() => void read(), 650);
          return;
        }
        setRun(previous => previous?.id === runId && previous.state_version > value.state_version ? previous : value);
        setRuns(previous => [value, ...previous.filter(item => item.id !== value.id)].slice(0, 100));
        if (!canAutoAdvance(value)) pause();
        if (loadingResult(value)) timer = window.setTimeout(() => void read(), 650);
      } catch (issue) {
        if (controller.signal.aborted) return;
        if (generation !== actionGeneration.current) {
          timer = window.setTimeout(() => void read(), 650);
        } else {
          pause();
          setError(errorText(issue));
        }
      }
    };
    void read();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [runId, readVersion, current?.status === 'queued' || current?.status === 'running', pause]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      actionGeneration.current += 1;
      window.clearTimeout(advanceTimer.current);
      onInteractionLock(false);
    };
  }, [onInteractionLock]);

  const change = useCallback(async (perform: () => Promise<Run>, showLocalError = false) => {
    if (actionInProgress.current) return;
    actionInProgress.current = true;
    const generation = ++actionGeneration.current;
    setBusy(true); setError(null); onInteractionLock(true);
    try {
      const value = await perform();
      if (!mounted.current || generation !== actionGeneration.current) return;
      if (!canAutoAdvance(value)) pause();
      setRun(value);
      setRuns(previous => [value, ...previous.filter(item => item.id !== value.id)].slice(0, 100));
      if (value.id !== runId) openRun(value.id);
    } catch (issue) {
      if (!mounted.current || generation !== actionGeneration.current) return;
      pause();
      if (!(issue instanceof ServiceError) || issue.status === 0 || issue.status === 409) setReadVersion(value => value + 1);
      if (showLocalError) throw issue;
      setError(errorText(issue));
    } finally {
      if (mounted.current && generation === actionGeneration.current) {
        actionInProgress.current = false;
        setBusy(false); onInteractionLock(false);
      }
    }
  }, [onInteractionLock, pause, runId]);

  const step = useCallback(() => {
    if (current) void change(() => runAction(current.id, 'step', { state_version: current.state_version }));
  }, [change, current]);

  useEffect(() => {
    if (!current || !playing || busy || error || document.hidden || !canAutoAdvance(current)) return;
    const timer = window.setTimeout(() => { if (!document.hidden) step(); }, playbackDelay);
    advanceTimer.current = timer;
    return () => window.clearTimeout(timer);
  }, [busy, current, error, playing, step]);

  return { current, runs, registryState, registryError, error, setError, busy, playing, setPlaying,
    pause, change, step, refresh: () => setReadVersion(value => value + 1) };
}
