// The execution catalog and one combination's verdict, read for a picker (lib/execution.ts). A hub without the routes
// (older than the four dimensions) answers 404: the catalog is then null and the pickers keep their launcher lists.
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../../lib/api";
import { cleanSpec, executionCatalog, validateExecution, type CombinationVerdict, type ExecutionCatalog, type ExecutionSpec } from "../../lib/execution";

export interface CatalogState {
  catalog: ExecutionCatalog | null;
  loading: boolean;
  error: string;
  /** The hub has no catalog (404: older than the four dimensions). */
  missing: boolean;
  reload: () => void;
}

/** GET /execution/catalog (?machine=). `controlled`: a catalog given by the caller (tests, a parent that read it). */
export function useExecutionCatalog(machine: string | undefined, enabled = true, controlled?: ExecutionCatalog | null): CatalogState {
  const [catalog, setCatalog] = useState<ExecutionCatalog | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [round, setRound] = useState(0);
  const on = enabled && controlled === undefined;
  useEffect(() => {
    if (!on) return;
    let live = true;
    setLoading(true);
    setError("");
    setMissing(false);
    executionCatalog(machine || undefined)
      .then((c) => live && setCatalog(c && Array.isArray(c.runtimes) ? c : null))
      .catch((e) => {
        if (!live) return;
        setCatalog(null);
        const gone = e instanceof ApiError && e.status === 404;
        setMissing(gone);
        setError(gone ? "" : errorText(e));
      })
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [on, machine, round]);
  const reload = useCallback(() => setRound((n) => n + 1), []);
  if (controlled !== undefined) return { catalog: controlled, loading: false, error: "", missing: controlled === null, reload };
  return { catalog, loading: on && loading, error, missing, reload };
}

/** POST /execution/validate for `spec` on `machine`, a moment after it last changed; null until answered, and when the
 *  hub could not say (a check that fails blocks nothing). `known`: a verdict the caller has already (the catalog's). */
export function useVerdict(spec: ExecutionSpec | null, machine: string | undefined, known?: CombinationVerdict | null): { verdict: CombinationVerdict | null; checking: boolean } {
  const [verdict, setVerdict] = useState<CombinationVerdict | null>(null);
  const [checking, setChecking] = useState(false);
  const key = spec ? JSON.stringify([cleanSpec(spec), machine ?? ""]) : "";
  const asked = useRef("");
  useEffect(() => {
    asked.current = key;
    if (!spec || known) {
      setVerdict(null);
      setChecking(false);
      return;
    }
    setChecking(true);
    const timer = setTimeout(() => {
      validateExecution(spec, machine || undefined)
        .then((v) => asked.current === key && setVerdict(v && typeof v.level === "string" ? v : null))
        .catch(() => asked.current === key && setVerdict(null))
        .finally(() => asked.current === key && setChecking(false));
    }, 200);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `key` stands for spec and machine
  }, [key, known]);
  return { verdict: known ?? verdict, checking: known ? false : checking };
}
