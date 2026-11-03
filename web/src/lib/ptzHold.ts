import type { PTZMoveRequest } from './api/cameras';

// A gesture owns its device ID through its injected callbacks. Stop is queued
// behind the last move, including when the view is destroyed mid-request.
export class PtzHold {
  private active = false;
  private disposed = false;
  private moving: Promise<void> | null = null;
  private stopping: Promise<void> | null = null;
  private timer: ReturnType<typeof setInterval> | null = null;

  constructor(
    private move: (request: PTZMoveRequest) => Promise<unknown>,
    private halt: () => Promise<unknown>,
    private onError: (error: unknown) => void,
    private renewMs = 0,
  ) {}

  start(request: PTZMoveRequest): boolean {
    if (this.disposed || this.active || this.moving || this.stopping) return false;
    this.active = true;
    const send = () => {
      if (!this.active || this.moving) return;
      const pending = Promise.resolve().then(() => this.move(request)).then(() => {}).catch(error => {
        if (!this.disposed) this.onError(error);
        void this.stop();
      }).finally(() => { if (this.moving === pending) this.moving = null; });
      this.moving = pending;
    };
    send();
    if (this.renewMs > 0) this.timer = setInterval(send, this.renewMs);
    return true;
  }

  stop(): Promise<void> {
    if (this.stopping) return this.stopping;
    if (!this.active && !this.moving) return Promise.resolve();
    this.active = false;
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
    const pending = this.moving;
    this.stopping = (async () => {
      if (pending) await pending;
      try { await this.halt(); }
      catch (error) { if (!this.disposed) this.onError(error); }
    })().finally(() => { this.stopping = null; });
    return this.stopping;
  }

  dispose(): Promise<void> {
    this.disposed = true;
    return this.stop();
  }
}
