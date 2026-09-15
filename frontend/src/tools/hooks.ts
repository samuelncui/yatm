import { useEffect, useState, RefObject } from "react";

type ObserverCallback = (visible: boolean) => void;
const observerCallbacks = new Map<Element, ObserverCallback>();

let sharedObserver: IntersectionObserver | null = null;

function getSharedObserver() {
  if (!sharedObserver) {
    sharedObserver = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        const callback = observerCallbacks.get(entry.target);
        if (callback) {
          callback(entry.isIntersecting);
        }
      });
    });
  }
  return sharedObserver;
}

export function useSharedIntersectionObserver(ref: RefObject<Element | null>): boolean {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const target = ref.current;
    if (!target) return;

    observerCallbacks.set(target, setVisible);
    getSharedObserver().observe(target);

    return () => {
      const observer = getSharedObserver();
      observer.unobserve(target);
      observerCallbacks.delete(target);
    };
  }, [ref]);

  return visible;
}
