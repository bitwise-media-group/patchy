// The findings list as the page holds it: the rows loaded so far for the
// current selection, a page at a time from GET /api/findings.
//
// A new selection replaces the rows (any request for the old one is
// cancelled). Scrolling appends the next page by cursor. A refresh (the SSE
// change signal, or after an action) refetches the first page: it swaps in
// at once while the reader is still on that page, but once they have
// scrolled further the new rows are held back as `pending` (the counts
// still update) so the list never reorders under someone reading it.

import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { fetchFindings } from "./api";
import { selectionQuery, type Selection } from "./filters";
import type { Persona } from "./mock/personas";
import type { FindingFacets, FindingSummary, FindingsPage } from "./types";

export const PAGE_SIZE = 50;

// MAX_ROWS bounds how many rows the page will hold. Past it, scrolling
// stops loading and the table asks for a narrower filter instead: nobody
// reads ten thousand rows, and every row is DOM the browser keeps.
export const MAX_ROWS = 5000;

interface Rows {
  // key is the query these rows answer.
  key: string;
  rows: FindingSummary[];
  matched: number;
  nextCursor?: string;
}

export interface FindingsList {
  rows: FindingSummary[];
  // matched counts the selection's findings across every page.
  matched: number;
  facets: FindingFacets | null;
  // loaded is false until the first page has arrived.
  loaded: boolean;
  // switching is true while a new selection's first page is in flight.
  switching: boolean;
  hasMore: boolean;
  loadingMore: boolean;
  moreFailed: boolean;
  // pending reports newer rows held back while the reader is scrolled.
  pending: boolean;
  loadMore: () => void;
  refresh: () => void;
  showPending: () => void;
}

function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

export function useFindingsList(
  selection: Selection,
  persona: Persona,
  // onPage receives every first page (the top bar's header rides on it).
  onPage: (page: FindingsPage) => void,
  // onFailure receives a first-page error, or an auth error on any page.
  onFailure: (e: unknown) => void,
  isAuthError: (e: unknown) => boolean,
): FindingsList {
  const query = useMemo(() => selectionQuery(selection), [selection]);
  const key = useMemo(() => JSON.stringify(query), [query]);

  const [current, setCurrent] = useState<Rows | null>(null);
  const [pending, setPending] = useState<Rows | null>(null);
  const [facets, setFacets] = useState<FindingFacets | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreFailed, setMoreFailed] = useState(false);

  const currentRef = useRef(current);
  currentRef.current = current;
  const firstCtl = useRef<AbortController | null>(null);
  const moreCtl = useRef<AbortController | null>(null);

  const cancelMore = () => {
    moreCtl.current?.abort();
    moreCtl.current = null;
    setLoadingMore(false);
  };

  const fetchFirst = useCallback(
    async (mode: "replace" | "refresh") => {
      firstCtl.current?.abort();
      const ctl = new AbortController();
      firstCtl.current = ctl;
      try {
        const page = await fetchFindings({ ...query, limit: PAGE_SIZE }, persona, ctl.signal);
        if (ctl.signal.aborted) return;
        onPage(page);
        if (page.facets) setFacets(page.facets);
        const fresh: Rows = { key, rows: page.findings, matched: page.matched, nextCursor: page.nextCursor };
        const prev = currentRef.current;
        const scrolled = prev !== null && prev.key === key && prev.rows.length > PAGE_SIZE;
        if (mode === "refresh" && scrolled) {
          setPending(fresh);
          setCurrent({ ...prev, matched: page.matched });
          return;
        }
        // A next page in flight continues the old first page; drop it.
        cancelMore();
        setPending(null);
        setMoreFailed(false);
        setCurrent(fresh);
      } catch (e) {
        if (!ctl.signal.aborted && !isAbort(e)) onFailure(e);
      } finally {
        if (firstCtl.current === ctl) firstCtl.current = null;
      }
    },
    [query, key, persona, onPage, onFailure],
  );

  // A new selection (or persona, in the demo) starts the list over.
  useEffect(() => {
    void fetchFirst("replace");
  }, [fetchFirst]);
  useEffect(
    () => () => {
      firstCtl.current?.abort();
      moreCtl.current?.abort();
    },
    [],
  );

  const loadMore = useCallback(async () => {
    const cur = currentRef.current;
    if (!cur || cur.key !== key || !cur.nextCursor || moreCtl.current || cur.rows.length >= MAX_ROWS) return;
    const ctl = new AbortController();
    moreCtl.current = ctl;
    setLoadingMore(true);
    setMoreFailed(false);
    try {
      const page = await fetchFindings({ ...query, limit: PAGE_SIZE, cursor: cur.nextCursor }, persona, ctl.signal);
      if (ctl.signal.aborted) return;
      setCurrent((prev) => {
        if (!prev || prev.key !== cur.key || prev.nextCursor !== cur.nextCursor) return prev;
        // Keyset paging never repeats a row, but a refresh swapped in
        // between could; never render one twice.
        const seen = new Set(prev.rows.map((f) => f.name));
        return {
          ...prev,
          rows: [...prev.rows, ...page.findings.filter((f) => !seen.has(f.name))],
          matched: page.matched,
          nextCursor: page.nextCursor,
        };
      });
    } catch (e) {
      if (ctl.signal.aborted || isAbort(e)) return;
      if (isAuthError(e)) onFailure(e);
      else setMoreFailed(true);
    } finally {
      if (moreCtl.current === ctl) {
        moreCtl.current = null;
        setLoadingMore(false);
      }
    }
  }, [query, key, persona, onFailure, isAuthError]);

  const refresh = useCallback(() => void fetchFirst("refresh"), [fetchFirst]);

  const showPending = useCallback(() => {
    if (!pending) return;
    cancelMore();
    setMoreFailed(false);
    setCurrent(pending);
    setPending(null);
  }, [pending]);

  const rows = current?.rows ?? [];
  return {
    rows,
    matched: current?.matched ?? 0,
    facets,
    loaded: current !== null,
    switching: current !== null && current.key !== key,
    hasMore: Boolean(current?.nextCursor) && rows.length < MAX_ROWS,
    loadingMore,
    moreFailed,
    pending: pending !== null && pending.key === key,
    loadMore: () => void loadMore(),
    refresh,
    showPending,
  };
}
