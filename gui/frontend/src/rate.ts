// RateEstimator turns the progress updates of one transfer into a smoothed
// speed and a time left. Go sends neither; updates arrive at most ten times a
// second, so the raw speed between two of them jumps around.

// Time constant of the smoothing, in milliseconds.
const SMOOTHING_MS = 3000;
// Samples closer than this are folded into the next one.
const MIN_SAMPLE_MS = 100;

export class RateEstimator {
    private lastAt = -1;
    private lastSent = 0;
    private speed = 0; // bytes per second
    private samples = 0;

    // add records that sent bytes had been sent at time nowMs.
    add(sent: number, nowMs: number): void {
        if (this.lastAt < 0 || sent < this.lastSent) {
            // The first update, or the counter went back: start over.
            this.lastAt = nowMs;
            this.lastSent = sent;
            this.speed = 0;
            this.samples = 0;
            return;
        }
        const dt = nowMs - this.lastAt;
        if (dt < MIN_SAMPLE_MS) {
            return;
        }
        const instant = ((sent - this.lastSent) * 1000) / dt;
        if (this.samples === 0) {
            this.speed = instant;
        } else {
            const alpha = 1 - Math.exp(-dt / SMOOTHING_MS);
            this.speed += alpha * (instant - this.speed);
        }
        this.samples++;
        this.lastAt = nowMs;
        this.lastSent = sent;
    }

    // bytesPerSecond is the smoothed speed, or null before it is known.
    bytesPerSecond(): number | null {
        return this.samples > 0 && this.speed >= 1 ? this.speed : null;
    }

    // secondsLeft estimates the time to send the remaining bytes, or null when
    // the speed is not known.
    secondsLeft(sent: number, total: number): number | null {
        const speed = this.bytesPerSecond();
        if (speed === null || total <= sent) {
            return null;
        }
        return (total - sent) / speed;
    }
}
