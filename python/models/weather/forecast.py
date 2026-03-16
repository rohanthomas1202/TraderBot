"""
Base forecast model interface.

All forecasting models implement this interface so that the backtest runner,
calibration tracker, and signal aggregator can treat them uniformly.
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass, field


@dataclass
class Forecast:
    """Output of a single forecast for one market contract."""

    market_id: str
    model_prob: float  # Calibrated P(event occurs)
    uncertainty: float  # +/- 1 sigma confidence band on model_prob
    raw_prob: float  # Pre-calibration probability
    model_version: str  # e.g., "weather-emos-v1"
    features_used: dict = field(default_factory=dict)  # For audit/debug
    forecast_time: str = ""  # ISO timestamp


@dataclass
class TradeSignal:
    """A sized trade recommendation from a model."""

    market_id: str
    side: str  # "buy" or "sell"
    quantity: int
    price_micros: int  # 0 to 1_000_000
    edge: float  # Net edge after fees
    model_prob: float
    market_prob: float
    uncertainty: float
    model_version: str
    reason: str
    category: str = ""


class ForecastModel(ABC):
    """Base class for all forecasting models."""

    @abstractmethod
    def forecast(self, contract: dict, features: dict) -> Forecast:
        """
        Produce a calibrated probability for a single contract.

        Args:
            contract: Contract metadata (market_id, threshold, expiry, etc.)
            features: Current feature values (ensemble data, observations, etc.)

        Returns:
            Forecast with calibrated probability and uncertainty.
        """

    @abstractmethod
    def train(self, training_data: dict) -> None:
        """
        Train or retrain the model on historical data.

        Args:
            training_data: Dict containing feature arrays and outcome labels.
        """

    @abstractmethod
    def save(self, path: str) -> None:
        """Serialize model to disk."""

    @classmethod
    @abstractmethod
    def load(cls, path: str) -> "ForecastModel":
        """Deserialize model from disk."""

    @property
    @abstractmethod
    def version(self) -> str:
        """Model version identifier (e.g., 'weather-emos-v1')."""
