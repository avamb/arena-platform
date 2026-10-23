/**
 * Binds the poster step state machine (lib/posterIntake.ts) to the wizard:
 * one intake per organization AND draft, the early upload of a chosen picture,
 * and the handlers of the poster block. The picture is uploaded once, here; the
 * draft keeps its media id and "Create event" only attaches it.
 *
 * The explicit read is a paid request: nothing here calls it except `read()`,
 * which the "Suggest details" button and "Read again" call.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { readPosterFacts, uploadPosterMedia } from "@/lib/api/endpoints";
import { createIntake, type Intake, type IntakeView } from "@/lib/posterIntake";
import { recordPosterProgress, type DraftAction } from "@/lib/wizard/draft";
import type { usePosterPicker } from "@/components/PosterPicker";

const IDLE: IntakeView = { upload: { phase: "idle" }, read: { phase: "idle" } };

export interface WizardPoster {
  view: IntakeView;
  /** A new picture was chosen: whatever was uploaded or read for the old one is dropped. */
  choose: (file: File) => void;
  /** The picture was removed: back to "not decided". */
  remove: () => void;
  /** "Continue without a poster", on purpose. */
  skip: () => void;
  retryUpload: () => void;
  read: () => void;
  /** Dismiss or finish the review (nothing filled in when `applied` is empty). */
  finishRead: (applied: readonly string[]) => void;
}

export function useWizardPoster(args: {
  orgId: string;
  draftId: string;
  mediaId: string;
  canChange: boolean;
  picker: ReturnType<typeof usePosterPicker>;
  dispatch: (action: DraftAction) => void;
  /** The poster is being sent with "Create event": the choice cannot change under the request. */
  locked: boolean;
}): WizardPoster {
  const { orgId, draftId, canChange, picker, dispatch, locked } = args;
  const [view, setView] = useState<IntakeView>(IDLE);
  const intakeRef = useRef<Intake | null>(null);
  const latest = useRef({ draftId, mediaId: args.mediaId, dispatch });
  latest.current = { draftId, mediaId: args.mediaId, dispatch };

  // One intake per organization and draft: another draft revokes pending answers, like the picker.
  useEffect(() => {
    const intake = createIntake({
      orgId,
      api: {
        upload: (file, type, signal) => uploadPosterMedia(orgId, file, type, signal),
        readFacts: (org, mediaId, signal) => readPosterFacts(org, mediaId, signal),
      },
      onUploaded: (mediaId) => {
        const { draftId: id, dispatch: d } = latest.current;
        // Stored at once under the draft's own id: a later failure must not lose the id.
        recordPosterProgress(orgId, id, { mediaId });
        d({ type: "posterUploaded", mediaId });
      },
    });
    intakeRef.current = intake;
    const off = intake.subscribe(setView);
    // A draft that already holds an uploaded picture: no upload, and above all NO read after a reload.
    if (latest.current.mediaId !== "") intake.adopt(latest.current.mediaId);
    setView(intake.view());
    return () => {
      off();
      intake.dispose();
      if (intakeRef.current === intake) intakeRef.current = null;
    };
  }, [orgId, draftId]);

  // The early upload: as soon as a checked picture is ready and nothing is stored for it.
  const ready = picker.state.phase === "ready" ? picker.state : null;
  useEffect(() => {
    if (ready === null || !canChange) return;
    const intake = intakeRef.current;
    if (intake === null || intake.view().upload.phase !== "idle") return;
    void intake.upload(ready.file, ready.info.type);
    // The picker state object changes with every new choice.
  }, [ready, canChange, view.upload.phase]);

  const choose = useCallback(
    (file: File) => {
      if (locked) return;
      intakeRef.current?.reset();
      dispatch({ type: "posterCleared" });
      picker.select(file);
    },
    [locked, dispatch, picker],
  );
  const remove = useCallback(() => {
    if (locked) return;
    intakeRef.current?.reset();
    dispatch({ type: "posterCleared" });
    picker.clear();
  }, [locked, dispatch, picker]);
  const skip = useCallback(() => {
    if (locked) return;
    intakeRef.current?.reset();
    dispatch({ type: "posterSkipped" });
    picker.clear();
  }, [locked, dispatch, picker]);
  const retryUpload = useCallback(() => {
    const s = picker.state;
    if (locked || s.phase !== "ready") return;
    void intakeRef.current?.upload(s.file, s.info.type);
  }, [locked, picker.state]);
  const read = useCallback(() => {
    if (locked) return;
    void intakeRef.current?.read();
  }, [locked]);
  const finishRead = useCallback((applied: readonly string[]) => intakeRef.current?.finishRead(applied), []);

  return { view, choose, remove, skip, retryUpload, read, finishRead };
}
