import { useCallback, useState } from "react";

export type DraftField<T> = {
  value?: T;
  change: (next?: T, expected?: T) => void;
};

// A late receipt can clear only the submitted draft, even after project switching.
export function useProjectDrafts<T>() {
  const [items, setItems] = useState<Record<string, T>>({});
  const change = useCallback((id: string, next?: T, expected?: T) => {
    setItems((current) => {
      if ((expected && current[id] !== expected) || (!next && !current[id]))
        return current;
      const updated = { ...current };
      if (next) updated[id] = next;
      else delete updated[id];
      return updated;
    });
  }, []);
  return { items, change, clear: setItems };
}
