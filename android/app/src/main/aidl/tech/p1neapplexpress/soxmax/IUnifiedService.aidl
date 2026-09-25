package tech.p1neapplexpress.soxmax;

interface IUnifiedService {
    boolean isVpnRunning();
    void stopVpn();

    boolean isFServiceRunning();
    void stopOpenFluxNative();
    void startOpenFluxNative(String transportType, in String[] args);
    void startTun2Socks();
    int getFd();
}