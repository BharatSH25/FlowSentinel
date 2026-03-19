FROM python:3.12-slim
WORKDIR /app

ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

RUN adduser --disabled-password --gecos "" appuser

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .
USER appuser
EXPOSE 8001
CMD ["uvicorn", "flowsentinel.admin.main:app", "--host", "0.0.0.0", "--port", "8001"]

