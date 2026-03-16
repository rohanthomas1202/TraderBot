"""
Isotonic Regression Calibrator

Takes raw probabilities from the EMOS model and maps them to calibrated
probabilities using isotonic regression trained on historical
(forecast, outcome) pairs.

Isotonic regression is ideal for probability calibration because:
1. It is monotone — if the raw model says A > B, calibrated will too
2. It is non-parametric — no distributional assumptions
3. It is fast to train and apply
4. It naturally handles the [0, 1] probability range
"""

import logging
import pickle
from pathlib import Path

import numpy as np
from sklearn.isotonic import IsotonicRegression

logger = logging.getLogger(__name__)

# Never predict absolute certainty — clamp to [PROB_FLOOR, PROB_CEIL]
PROB_FLOOR = 0.02
PROB_CEIL = 0.98


class IsotonicCalibrator:
    """
    Calibrates raw probabilities using isotonic regression.

    Usage:
        cal = IsotonicCalibrator()
        cal.fit(raw_probs, outcomes)        # outcomes: 1 = event occurred, 0 = did not
        calibrated = cal.calibrate(0.65)    # calibrated probability
    """

    def __init__(self, prob_floor: float = PROB_FLOOR, prob_ceil: float = PROB_CEIL):
        self.prob_floor = prob_floor
        self.prob_ceil = prob_ceil
        self._iso = IsotonicRegression(
            y_min=prob_floor,
            y_max=prob_ceil,
            out_of_bounds="clip",
        )
        self._fitted = False

    def fit(self, raw_probs: np.ndarray, outcomes: np.ndarray) -> "IsotonicCalibrator":
        """
        Train the calibrator on historical (raw_prob, outcome) pairs.

        Args:
            raw_probs: Array of raw model probabilities in [0, 1].
            outcomes: Array of binary outcomes (1 = event occurred).

        Returns:
            self, for chaining.
        """
        raw_probs = np.asarray(raw_probs, dtype=np.float64)
        outcomes = np.asarray(outcomes, dtype=np.float64)

        if len(raw_probs) != len(outcomes):
            raise ValueError("raw_probs and outcomes must have same length")
        if len(raw_probs) < 20:
            raise ValueError(f"Need at least 20 samples for calibration, got {len(raw_probs)}")

        # Sort by raw probability (isotonic regression requires this)
        sort_idx = np.argsort(raw_probs)
        self._iso.fit(raw_probs[sort_idx], outcomes[sort_idx])
        self._fitted = True

        logger.info(
            "Calibrator fitted on %d samples (%.1f%% positive rate)",
            len(raw_probs),
            100 * outcomes.mean(),
        )
        return self

    def calibrate(self, raw_prob: float | np.ndarray) -> float | np.ndarray:
        """
        Map raw probability to calibrated probability.

        Args:
            raw_prob: Raw model probability (scalar or array).

        Returns:
            Calibrated probability, clamped to [prob_floor, prob_ceil].
        """
        if not self._fitted:
            raise RuntimeError("Calibrator not fitted. Call fit() first.")

        result = self._iso.predict(np.atleast_1d(raw_prob))
        result = np.clip(result, self.prob_floor, self.prob_ceil)

        if np.isscalar(raw_prob) or (isinstance(raw_prob, np.ndarray) and raw_prob.ndim == 0):
            return float(result[0])
        return result

    def calibration_error(self, raw_probs: np.ndarray, outcomes: np.ndarray, n_bins: int = 10) -> float:
        """
        Compute Expected Calibration Error (ECE).

        Bins predictions by predicted probability and compares mean prediction
        to observed frequency in each bin.
        """
        calibrated = self.calibrate(raw_probs)
        bins = np.linspace(0, 1, n_bins + 1)
        ece = 0.0
        total = len(calibrated)

        for i in range(n_bins):
            mask = (calibrated >= bins[i]) & (calibrated < bins[i + 1])
            if mask.sum() == 0:
                continue
            bin_pred = calibrated[mask].mean()
            bin_actual = outcomes[mask].mean()
            ece += (mask.sum() / total) * abs(bin_pred - bin_actual)

        return ece

    def reliability_data(self, raw_probs: np.ndarray, outcomes: np.ndarray, n_bins: int = 10) -> dict:
        """
        Compute data for a reliability diagram.

        Returns dict with bin_centers, observed_freq, predicted_mean, and counts
        for plotting.
        """
        calibrated = self.calibrate(raw_probs)
        bins = np.linspace(0, 1, n_bins + 1)

        bin_centers = []
        observed_freq = []
        predicted_mean = []
        counts = []

        for i in range(n_bins):
            mask = (calibrated >= bins[i]) & (calibrated < bins[i + 1])
            n = mask.sum()
            if n == 0:
                continue
            bin_centers.append((bins[i] + bins[i + 1]) / 2)
            observed_freq.append(float(outcomes[mask].mean()))
            predicted_mean.append(float(calibrated[mask].mean()))
            counts.append(int(n))

        return {
            "bin_centers": bin_centers,
            "observed_freq": observed_freq,
            "predicted_mean": predicted_mean,
            "counts": counts,
        }

    def save(self, path: str) -> None:
        with open(path, "wb") as f:
            pickle.dump({"iso": self._iso, "floor": self.prob_floor, "ceil": self.prob_ceil}, f)

    @classmethod
    def load(cls, path: str) -> "IsotonicCalibrator":
        with open(path, "rb") as f:
            data = pickle.load(f)
        cal = cls(prob_floor=data["floor"], prob_ceil=data["ceil"])
        cal._iso = data["iso"]
        cal._fitted = True
        return cal
