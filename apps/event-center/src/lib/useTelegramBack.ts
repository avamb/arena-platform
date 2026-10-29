import { useEffect, useRef } from "react";
import { bindBackButton, loadTelegramScript } from "@/lib/tgEdit";

/**
 * Shows the Telegram back button for as long as the page is mounted and runs
 * `onPress` on a press. The button is bound only once the Telegram script has
 * loaded: the script is fetched after the page is drawn, and a bind before it
 * is a bind to nothing (the button would never appear). The handler is read
 * through a ref, so a new function on every render does not take the button
 * away and put it back.
 */
export function useTelegramBack(onPress: () => void): void {
  const press = useRef(onPress);
  press.current = onPress;
  useEffect(() => {
    let alive = true;
    let unbind: () => void = () => undefined;
    void loadTelegramScript().then(() => {
      if (alive) unbind = bindBackButton(() => press.current());
    });
    return () => {
      alive = false;
      unbind();
    };
  }, []);
}
