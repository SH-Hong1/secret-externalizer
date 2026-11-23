public class Partner {
    static final String TOKEN_HEADER = "X-Auth-Token";
    static final String MSG_PWD = "Enter the password again";
    static final String PASSWORD_PARAM = "j_password";
    static final String API_TOKEN = "dev-only-fake-api-token";

    void call(java.net.URLConnection conn) {
        conn.setRequestProperty("Cookie", "PARTNER_SID=devonlyfakejavacookie");
    }
}
