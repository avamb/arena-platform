/**
 * AddPaymentInfo for server-side Meta / GA4 (spec 68 §3.2): when the Payment
 * Element reports complete=true (the step `payment_details_complete`), the
 * platform is told once per checkout token. Best effort, and only with the
 * buyer's explicit tracking yes.
 */
import type { PaymentStep } from './paymentSteps.js';

export interface PaymentInfoNotifier {
  onStep(step: PaymentStep, token: string): void;
}

export function createPaymentInfoNotifier(opts: {
  granted: () => boolean;
  send: (token: string) => Promise<unknown>;
}): PaymentInfoNotifier {
  const sent = new Set<string>();
  return {
    onStep(step, token) {
      if (step !== 'payment_details_complete' || !token || sent.has(token)) return;
      if (!opts.granted()) return;
      sent.add(token);
      try {
        void opts.send(token).catch(() => {});
      } catch {
        // best effort
      }
    },
  };
}
