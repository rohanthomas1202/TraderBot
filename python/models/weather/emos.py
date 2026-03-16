"""
EMOS (Ensemble Model Output Statistics) Weather Model

Implements Non-homogeneous Gaussian Regression (NGR) to combine GFS and
ECMWF ensemble forecasts into a single calibrated probability distribution.

Algorithm:
1. Combine GFS + ECMWF ensemble members into a parametric Gaussian:
   - mu = a + b1 * GFS_mean + b2 * ECMWF_mean
   - sigma = c + d * ensemble_spread
2. Compute P(metric > threshold) from the fitted distribution
3. Pass through isotonic regression calibrator
4. Output calibrated probability with uncertainty band

Training uses CRPS (Continuous Ranked Probability Score) minimization
via scipy.optimize, which is the standard loss function for probabilistic
weather forecast calibration.
"""

import logging
import pickle
from datetime import datetime
from pathlib import Path

import numpy as np
from scipy import optimize, stats

from models.weather.calibration import IsotonicCalibrator
from models.weather.forecast import Forecast, ForecastModel

logger = logging.getLogger(__name__)

# Minimum ensemble spread to prevent overconfidence
SIGMA_FLOOR_F = 0.5  # degrees Fahrenheit


def _crps_gaussian(mu: float, sigma: float, observation: float) -> float:
    """
    Compute CRPS (Continuous Ranked Probability Score) for a Gaussian forecast.

    CRPS is the standard scoring rule for probabilistic forecasts.
    Lower is better. For a Gaussian distribution, it has a closed-form solution.
    """
    sigma = max(sigma, 1e-6)
    z = (observation - mu) / sigma
    crps = sigma * (z * (2 * stats.norm.cdf(z) - 1) + 2 * stats.norm.pdf(z) - 1 / np.sqrt(np.pi))
    return crps


class EMOSModel(ForecastModel):
    """
    EMOS weather forecasting model.

    Produces calibrated probabilities for binary weather contracts
    (e.g., "Will NYC high temperature exceed 85F on March 20?").
    """

    def __init__(self, model_version: str = "weather-emos-v1", constant_sigma: bool = False):
        self._version = model_version
        self.constant_sigma = constant_sigma
        # NGR parameters: mu = a + b1*gfs_mean + b2*ecmwf_mean
        #                 sigma = c + d*ensemble_spread  (d unused when constant_sigma=True)
        self.params = {
            "a": 0.0,       # intercept
            "b_gfs": 0.8,   # GFS weight
            "b_ecmwf": 0.2, # ECMWF weight
            "c": 1.0,       # sigma intercept
            "d": 0.5,       # spread scaling (fixed to 0 in constant_sigma mode)
        }
        if self.constant_sigma:
            self.params["d"] = 0.0
        self.calibrator = IsotonicCalibrator()
        self._trained = False

    @property
    def version(self) -> str:
        return self._version

    def _predict_distribution(
        self,
        gfs_mean: float,
        gfs_spread: float,
        ecmwf_mean: float,
        ecmwf_spread: float | None = None,
    ) -> tuple[float, float]:
        """
        Predict Gaussian distribution parameters from ensemble inputs.

        Returns (mu, sigma) for the forecast distribution.
        """
        mu = (
            self.params["a"]
            + self.params["b_gfs"] * gfs_mean
            + self.params["b_ecmwf"] * ecmwf_mean
        )

        if self.constant_sigma:
            sigma = max(self.params["c"], SIGMA_FLOOR_F)
        else:
            # Use combined spread from both ensembles
            combined_spread = gfs_spread
            if ecmwf_spread is not None:
                combined_spread = np.sqrt(gfs_spread**2 + ecmwf_spread**2) / np.sqrt(2)

            sigma = max(
                self.params["c"] + self.params["d"] * combined_spread,
                SIGMA_FLOOR_F,
            )

        return mu, sigma

    def forecast(self, contract: dict, features: dict) -> Forecast:
        """
        Produce calibrated probability for a single weather contract.

        Args:
            contract: Must contain 'market_id', 'threshold' (in F), and
                      'direction' ("above" or "below").
            features: Must contain 'gfs_mean', 'gfs_std', and optionally
                      'ecmwf_mean', 'ecmwf_std'.

        Returns:
            Forecast with calibrated probability.
        """
        gfs_mean = features.get("gfs_mean", 0.0)
        gfs_std = features.get("gfs_std", 1.0)
        ecmwf_mean = features.get("ecmwf_mean", gfs_mean)
        ecmwf_std = features.get("ecmwf_std")

        mu, sigma = self._predict_distribution(gfs_mean, gfs_std, ecmwf_mean, ecmwf_std)

        threshold = contract.get("threshold", 0.0)
        direction = contract.get("direction", "above")

        # Raw probability from fitted Gaussian
        if direction == "above":
            raw_prob = 1.0 - stats.norm.cdf(threshold, loc=mu, scale=sigma)
        else:
            raw_prob = stats.norm.cdf(threshold, loc=mu, scale=sigma)

        # Calibrate
        if self._trained and self.calibrator._fitted:
            model_prob = self.calibrator.calibrate(raw_prob)
        else:
            model_prob = np.clip(raw_prob, 0.02, 0.98)

        # Uncertainty: based on ensemble spread relative to threshold distance
        distance_to_threshold = abs(mu - threshold) / sigma
        uncertainty = max(0.01, 0.15 * np.exp(-0.5 * distance_to_threshold))

        return Forecast(
            market_id=contract.get("market_id", ""),
            model_prob=float(model_prob),
            uncertainty=float(uncertainty),
            raw_prob=float(raw_prob),
            model_version=self._version,
            features_used={
                "gfs_mean": gfs_mean,
                "gfs_std": gfs_std,
                "ecmwf_mean": ecmwf_mean,
                "mu": mu,
                "sigma": sigma,
                "threshold": threshold,
            },
            forecast_time=datetime.utcnow().isoformat(),
        )

    def train(self, training_data: dict) -> None:
        """
        Train the EMOS model using CRPS minimization.

        Args:
            training_data: Dict with keys:
                - gfs_mean: array of GFS ensemble means
                - gfs_std: array of GFS ensemble standard deviations
                - ecmwf_mean: array of ECMWF values (ERA5 reanalysis or ensemble mean)
                - ecmwf_std: array of ECMWF spread (optional, can be None)
                - observations: array of actual observed values (ground truth)
                - thresholds: array of contract thresholds (for calibration)
                - outcomes: binary array (1 = event occurred) for calibration
        """
        gfs_means = np.asarray(training_data["gfs_mean"])
        gfs_stds = np.asarray(training_data["gfs_std"])
        ecmwf_means = np.asarray(training_data["ecmwf_mean"])
        observations = np.asarray(training_data["observations"])

        n = len(observations)
        logger.info("Training EMOS on %d samples", n)

        # Step 1: Optimize NGR parameters via CRPS minimization
        if self.constant_sigma:
            # 4-parameter model: d is fixed to 0 (no spread term)
            def crps_loss(params):
                a, b_gfs, b_ecmwf, c = params
                total_crps = 0.0
                for i in range(n):
                    mu = a + b_gfs * gfs_means[i] + b_ecmwf * ecmwf_means[i]
                    sigma = max(c, SIGMA_FLOOR_F)
                    total_crps += _crps_gaussian(mu, sigma, observations[i])
                return total_crps / n

            x0 = [self.params["a"], self.params["b_gfs"], self.params["b_ecmwf"], self.params["c"]]
            bounds = [(-20, 20), (0, 2), (0, 2), (0.1, 10)]

            result = optimize.minimize(
                crps_loss, x0, method="L-BFGS-B", bounds=bounds,
                options={"maxiter": 1000, "ftol": 1e-8},
            )

            if result.success:
                self.params["a"] = result.x[0]
                self.params["b_gfs"] = result.x[1]
                self.params["b_ecmwf"] = result.x[2]
                self.params["c"] = result.x[3]
                self.params["d"] = 0.0
                logger.info(
                    "CRPS optimization converged (constant_sigma): "
                    "a=%.3f, b_gfs=%.3f, b_ecmwf=%.3f, c=%.3f",
                    *result.x,
                )
            else:
                logger.warning("CRPS optimization did not converge: %s", result.message)
        else:
            # Full 5-parameter model with spread scaling
            def crps_loss(params):
                a, b_gfs, b_ecmwf, c, d = params
                total_crps = 0.0
                for i in range(n):
                    mu = a + b_gfs * gfs_means[i] + b_ecmwf * ecmwf_means[i]
                    sigma = max(c + d * gfs_stds[i], SIGMA_FLOOR_F)
                    total_crps += _crps_gaussian(mu, sigma, observations[i])
                return total_crps / n

            x0 = [
                self.params["a"], self.params["b_gfs"], self.params["b_ecmwf"],
                self.params["c"], self.params["d"],
            ]
            bounds = [(-20, 20), (0, 2), (0, 2), (0.1, 10), (0, 3)]

            result = optimize.minimize(
                crps_loss, x0, method="L-BFGS-B", bounds=bounds,
                options={"maxiter": 1000, "ftol": 1e-8},
            )

            if result.success:
                self.params["a"] = result.x[0]
                self.params["b_gfs"] = result.x[1]
                self.params["b_ecmwf"] = result.x[2]
                self.params["c"] = result.x[3]
                self.params["d"] = result.x[4]
                logger.info(
                    "CRPS optimization converged: a=%.3f, b_gfs=%.3f, b_ecmwf=%.3f, c=%.3f, d=%.3f",
                    *result.x,
                )
            else:
                logger.warning("CRPS optimization did not converge: %s", result.message)

        # Step 2: Train isotonic calibrator on threshold exceedance
        if "thresholds" in training_data and "outcomes" in training_data:
            thresholds = np.asarray(training_data["thresholds"])
            outcomes = np.asarray(training_data["outcomes"])

            raw_probs = np.zeros(len(thresholds))
            for i in range(len(thresholds)):
                mu = (
                    self.params["a"]
                    + self.params["b_gfs"] * gfs_means[i]
                    + self.params["b_ecmwf"] * ecmwf_means[i]
                )
                if self.constant_sigma:
                    sigma = max(self.params["c"], SIGMA_FLOOR_F)
                else:
                    sigma = max(
                        self.params["c"] + self.params["d"] * gfs_stds[i],
                        SIGMA_FLOOR_F,
                    )
                raw_probs[i] = 1.0 - stats.norm.cdf(thresholds[i], loc=mu, scale=sigma)

            self.calibrator.fit(raw_probs, outcomes)
            ece = self.calibrator.calibration_error(raw_probs, outcomes)
            logger.info("Calibrator trained: ECE = %.4f", ece)

        self._trained = True

    def evaluate(self, test_data: dict) -> dict:
        """
        Evaluate model on test data. Returns metrics dict.
        """
        gfs_means = np.asarray(test_data["gfs_mean"])
        gfs_stds = np.asarray(test_data["gfs_std"])
        ecmwf_means = np.asarray(test_data["ecmwf_mean"])
        observations = np.asarray(test_data["observations"])

        # CRPS on continuous forecasts
        crps_scores = []
        for i in range(len(observations)):
            mu, sigma = self._predict_distribution(
                gfs_means[i], gfs_stds[i], ecmwf_means[i]
            )
            crps_scores.append(_crps_gaussian(mu, sigma, observations[i]))

        result = {
            "mean_crps": float(np.mean(crps_scores)),
            "median_crps": float(np.median(crps_scores)),
            "n_samples": len(observations),
        }

        # Brier score on binary outcomes if thresholds provided
        if "thresholds" in test_data and "outcomes" in test_data:
            thresholds = np.asarray(test_data["thresholds"])
            outcomes = np.asarray(test_data["outcomes"])

            probs = []
            for i in range(len(thresholds)):
                contract = {"threshold": thresholds[i], "direction": "above"}
                features = {
                    "gfs_mean": gfs_means[i],
                    "gfs_std": gfs_stds[i],
                    "ecmwf_mean": ecmwf_means[i],
                }
                fc = self.forecast(contract, features)
                probs.append(fc.model_prob)

            probs = np.array(probs)
            brier = float(np.mean((probs - outcomes) ** 2))
            result["brier_score"] = brier

            if self.calibrator._fitted:
                result["ece"] = self.calibrator.calibration_error(probs, outcomes)
                result["reliability"] = self.calibrator.reliability_data(probs, outcomes)

        logger.info(
            "Evaluation: CRPS=%.4f, Brier=%.4f",
            result["mean_crps"],
            result.get("brier_score", float("nan")),
        )
        return result

    def save(self, path: str) -> None:
        data = {
            "version": self._version,
            "params": self.params,
            "calibrator": self.calibrator,
            "trained": self._trained,
            "constant_sigma": self.constant_sigma,
        }
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        with open(path, "wb") as f:
            pickle.dump(data, f)
        logger.info("Model saved to %s", path)

    @classmethod
    def load(cls, path: str) -> "EMOSModel":
        with open(path, "rb") as f:
            data = pickle.load(f)
        model = cls(
            model_version=data["version"],
            constant_sigma=data.get("constant_sigma", False),
        )
        model.params = data["params"]
        model.calibrator = data["calibrator"]
        model._trained = data["trained"]
        logger.info(
            "Model loaded from %s (version=%s, constant_sigma=%s)",
            path, model._version, model.constant_sigma,
        )
        return model
