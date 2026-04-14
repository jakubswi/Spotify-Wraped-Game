FROM python:3.10-slim

# Prevent python from writing pyc files and buffering stdout
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# Create a non-root user and group for better security
RUN addgroup --system appgroup && adduser --system --group appgroup

WORKDIR /app

# Install dependencies first for better caching
COPY app/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the rest of the application
COPY app/ .

# Change ownership of the app directory to the non-root user
RUN chown -R appgroup:appgroup /app

# Switch to the non-root user
USER appgroup

EXPOSE 5000

# Using gunicorn as a production WSGI server
CMD ["gunicorn", "--bind", "0.0.0.0:5000", "--workers", "3", "--access-logfile", "-", "--error-logfile", "-", "app:app"]
